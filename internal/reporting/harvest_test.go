// Tests for the harvest: a pass that may fetch the active slot stores a live
// credential Claude Code rotated itself into the attributed slot's backup.
package reporting

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

const harvestLine = "Harvested the rotated live credential into Account-5's backup"

var harvestLive = oauthCreds("at-5-new", "rt-5-rotated", 4102444800000)

// harvestFixture: slots 5 and 6 are one login in two organisations, the
// config names 6 and the credential file holds 5's token after Claude Code
// refreshed it; the profile endpoint answers with slot 5's identity.
func harvestFixture(t *testing.T) (*store.Store, *[]string) {
	t.Helper()
	var tokens []string
	s := newStore(t, nil, &oauth.FakeClient{
		ProfileFn: func(_ context.Context, token string) *oauth.Identity {
			if token == "at-5-new" {
				return &oauth.Identity{UUID: "acct-obi", Email: "obi@example.com", OrgUUID: "org-five"}
			}
			return nil
		},
		UsageFn: func(ctx context.Context, token string) (map[string]any, error) {
			tokens = append(tokens, token)
			return usageOK(ctx, token)
		},
	})
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 6,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [5, 6],
  "accounts": {
    "5": {"email": "obi@example.com", "uuid": "acct-obi", "organizationUuid": "org-five", "organizationName": "Five", "added": "t"},
    "6": {"email": "obi@example.com", "uuid": "acct-obi", "organizationUuid": "org-six", "organizationName": "Six", "added": "t"}
  }
}`)
	for _, num := range []string{"5", "6"} {
		writeBackup(t, s, num, "obi@example.com", oauthCreds("at-"+num, "rt-"+num, 4102444800000), `{"oauthAccount":{}}`)
	}
	writeActiveCreds(t, s, harvestLive)
	writeLiveConfig(t, s, "obi@example.com", "org-six")
	return s, &tokens
}

func liveCredentialBytes(t *testing.T, s *store.Store) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Home, ".claude", ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func storeLog(t *testing.T, s *store.Store) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func backupOf(t *testing.T, s *store.Store, num string) string {
	t.Helper()
	got, err := s.ReadAccountCredentials(num, "obi@example.com")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCollectHarvestsRotatedLiveCredential(t *testing.T) {
	s, tokens := harvestFixture(t)
	live := liveCredentialBytes(t, s)

	entries := UsageEntriesByAccount(s, nil)
	if got := backupOf(t, s, "5"); got != oauth.AccountOnly(harvestLive) || *oauth.CredentialFingerprint(got) != *oauth.CredentialFingerprint(harvestLive) {
		t.Fatalf("Account-5 backup = %s, want the rotated live credential", got)
	}
	if got := backupOf(t, s, "6"); got != oauthCreds("at-6", "rt-6", 4102444800000) {
		t.Fatalf("Account-6 backup = %s, want it untouched", got)
	}
	if got := liveCredentialBytes(t, s); got != live {
		t.Fatalf("the live credential file changed: %s", got)
	}
	log := storeLog(t, s)
	if !strings.Contains(log, "INFO") || strings.Count(log, harvestLine) != 1 {
		t.Fatalf("log lacks one INFO harvest line:\n%s", log)
	}
	if entries["5"].LastGood == nil || entries["5"].SkipReason != "" {
		t.Fatalf("slot 5 entry = %+v, want its usage fetched without a skip", entries["5"])
	}
	if len(*tokens) == 0 || (*tokens)[0] != "at-5-new" {
		t.Fatalf("usage requests used %v, want the live token for slot 5", *tokens)
	}

	UsageEntriesByAccount(s, nil)
	if n := strings.Count(storeLog(t, s), harvestLine); n != 1 {
		t.Fatalf("second pass: harvest lines = %d, want 1", n)
	}
	data, _ := s.ReadSequence()
	if ls := s.ResolveLiveSlot(data); ls.Slot != "5" || ls.Source != store.SourceBackup {
		t.Fatalf("after the harvest ResolveLiveSlot = %+v, want slot 5 by backup", ls)
	}
}

func TestCollectNeverHarvestsOnStoreOnlyRead(t *testing.T) {
	recordPath := func(s *store.Store) string { return filepath.Join(s.BackupDir(), "live_lineage.json") }
	untouched := func(t *testing.T, s *store.Store, what string) {
		t.Helper()
		if got := backupOf(t, s, "5"); got != oauthCreds("at-5", "rt-5", 4102444800000) {
			t.Fatalf("%s harvested into Account-5: %s", what, got)
		}
		if strings.Contains(storeLog(t, s), harvestLine) {
			t.Fatalf("%s logged a harvest", what)
		}
	}

	t.Run("store-only read", func(t *testing.T) {
		s, _ := harvestFixture(t)
		UsageEntriesByAccount(s, map[string]bool{})
		Snapshot(s, map[string]bool{})
		untouched(t, s, "a store-only read")
		if _, err := os.Stat(recordPath(s)); !os.IsNotExist(err) {
			t.Fatalf("a store-only read wrote live_lineage.json (stat err %v)", err)
		}
	})
	t.Run("active slot not wanted", func(t *testing.T) {
		s, _ := harvestFixture(t)
		UsageEntriesByAccount(s, map[string]bool{"6": true})
		untouched(t, s, "a pass that may not fetch the active slot")
	})
	t.Run("dry run", func(t *testing.T) {
		s, _ := harvestFixture(t)
		UsageEntriesByAccount(s.WithoutLineageWrites(), nil)
		untouched(t, s, "a dry run")
		if _, err := os.Stat(recordPath(s)); !os.IsNotExist(err) {
			t.Fatalf("a dry run wrote live_lineage.json (stat err %v)", err)
		}
	})
	t.Run("no client", func(t *testing.T) {
		s, _ := harvestFixture(t)
		data, _ := s.ReadSequence()
		s.ResolveLiveSlot(data)
		s.OAuth = nil
		if ls := s.ResolveLiveSlot(data); ls.Source != store.SourceChain {
			t.Fatalf("ResolveLiveSlot = %+v, want slot 5 by chain", ls)
		}
		UsageEntriesByAccount(s, nil)
		untouched(t, s, "a pass without a client")
	})
}

// countingCreds counts the active store's credential reads, outside the store
// lock and under it.
type countingCreds struct {
	credstore.Store
	lock            *filelock.FileLock
	outside, locked int
}

func (c *countingCreds) ReadActive() (string, bool, error) {
	if c.lock != nil && c.lock.Held() {
		c.locked++
	} else {
		c.outside++
	}
	return c.Store.ReadActive()
}

// A pass reads the live credential once outside the lock: the resolution and
// the active row share the read, and the harvest reuses both; only the
// harvest's re-check reads it again, under the lock.
func TestCollectReadsTheLiveCredentialOncePerPass(t *testing.T) {
	s, _ := harvestFixture(t)
	counter := &countingCreds{Store: s.Creds, lock: s.Lock}
	s.Creds = counter
	UsageEntriesByAccount(s, nil)
	if harvested := strings.Contains(storeLog(t, s), harvestLine); !harvested || counter.outside != 1 || counter.locked != 1 {
		t.Fatalf("harvest pass: harvested %v, reads outside the lock %d, under it %d; want a harvest, 1 and 1", harvested, counter.outside, counter.locked)
	}
	counter.outside, counter.locked = 0, 0
	UsageEntriesByAccount(s, nil)
	if counter.outside != 1 || counter.locked != 0 {
		t.Fatalf("steady pass: reads outside the lock %d, under it %d; want 1 and 0", counter.outside, counter.locked)
	}
}

// The status line hands its own resolution to the harvest.
func TestStatusHarvestsWithItsResolution(t *testing.T) {
	s, _ := harvestFixture(t)
	buildStatusPayload(s)
	if got := backupOf(t, s, "5"); got != oauth.AccountOnly(harvestLive) {
		t.Fatalf("Account-5 backup = %s, want the rotated live credential", got)
	}
}
