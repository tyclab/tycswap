package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

const dryEmail = "obi@example.com"

func dryCreds(access, refresh string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":4102444800000}}`
}

// rotatedSwitcher: ~/.claude.json names slot 6, the credential file holds
// slot 5's token after Claude Code rotated it (no backup matches), the profile
// endpoint names slot 5, and the Opus group has slot 7 active.
func rotatedSwitcher(t *testing.T) *core.Switcher {
	t.Helper()
	cleanHome(t)
	clk := testutil.FixedClock(t, "2026-07-17T09:00:00Z")
	sw, err := core.New(store.Options{Clock: clk, Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{},
		OAuth: &oauth.FakeClient{ProfileFn: func(_ context.Context, token string) *oauth.Identity {
			if token == "at-5-new" {
				return &oauth.Identity{UUID: "acct-obi", Email: dryEmail, OrgUUID: "org-five"}
			}
			return nil
		}}})
	if err != nil {
		t.Fatal(err)
	}
	sw.Creds = credstore.New(credstore.Config{Platform: platform.Linux, CredentialsDir: sw.CredentialsDir}, sw.Keychain(), clk, sw.Log)
	if err := sw.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	accounts := map[string]json.RawMessage{}
	for num, org := range map[string]string{"5": "org-five", "6": "org-six", "7": "org-seven"} {
		accounts[num], _ = json.Marshal(map[string]any{"email": dryEmail, "uuid": "acct-obi", "organizationUuid": org, "organizationName": org, "added": "t"})
		if err := sw.WriteAccountCredentials(num, dryEmail, dryCreds("at-"+num, "rt-"+num)); err != nil {
			t.Fatal(err)
		}
		if err := sw.WriteAccountConfig(num, dryEmail, `{"oauthAccount":{"emailAddress":"`+dryEmail+`","organizationUuid":"`+org+`"}}`); err != nil {
			t.Fatal(err)
		}
	}
	active := 6
	if err := sw.WriteSequence(&store.SequenceData{ActiveAccountNumber: &active, LastUpdated: "2026-07-17T09:00:00Z", Sequence: []int{5, 6, 7}, Accounts: accounts}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sw.Home, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"`+dryEmail+`","organizationUuid":"org-six"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sw.Home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sw.Home, ".claude", ".credentials.json"), []byte(dryCreds("at-5-new", "rt-5-rotated")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(groups.ScopeDir(sw.BackupDir(), groups.Opus), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := groups.WriteJSON(groups.ActivePath(sw.BackupDir(), groups.Opus), groups.Account{Number: "7", Email: dryEmail, OrgUUID: "org-seven"}); err != nil {
		t.Fatal(err)
	}
	return sw
}

func chainRecordExists(t *testing.T, sw *core.Switcher) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(sw.BackupDir(), "live_lineage.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// A dry tick, of the default engine or of the group scopes, never records the
// live credential's lineage nor harvests it (DESIGN A68); a real group tick
// does record it, so the fixture reaches the write.
func TestDryTicksWriteNoLineage(t *testing.T) {
	t.Run("engine switcher", func(t *testing.T) {
		sw := &core.Switcher{Store: &store.Store{}}
		if got := engineSwitcher(sw, false); got != sw {
			t.Fatalf("real engine switcher = %p, want sw %p", got, sw)
		}
		if dry := engineSwitcher(sw, true); dry.LineageWrites() || !sw.LineageWrites() {
			t.Fatalf("dry-run writes lineage = %v, sw writes lineage = %v; want false, true", dry.LineageWrites(), sw.LineageWrites())
		}
	})
	t.Run("default engine", func(t *testing.T) {
		sw := rotatedSwitcher(t)
		newAutoEngine(sw, settings.Default(), func(autoswitch.Event) {}, true, sw.OAuth).Tick()
		if chainRecordExists(t, sw) {
			t.Fatal("a dry engine tick wrote live_lineage.json")
		}
		if got, _ := sw.ReadAccountCredentials("5", dryEmail); got != dryCreds("at-5", "rt-5") {
			t.Fatalf("a dry engine tick harvested into Account-5: %s", got)
		}
	})
	t.Run("group scopes", func(t *testing.T) {
		sw := rotatedSwitcher(t)
		tickGroupScopes(sw, settings.Default(), nil, true)
		if chainRecordExists(t, sw) {
			t.Fatal("a dry group tick wrote live_lineage.json")
		}
	})
	t.Run("real group tick", func(t *testing.T) {
		sw := rotatedSwitcher(t)
		tickGroupScopes(sw, settings.Default(), nil, false)
		if !chainRecordExists(t, sw) {
			t.Fatal("a real group tick did not record the lineage: the fixture misses the write")
		}
	})
}
