// Tests for the skip reasons the active-account usage paths record (spec Part 2
// of the 2026-10-25 engine cross-tick state design): every sentinel return names
// why, a collect pass persists it, logs once per change, and a success clears it.
package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

const (
	skipEmail         = "a@example.com"
	wantOwnerExpired  = "owner holds an expired token; Claude Code refreshes it on next use"
	wantNoCredential  = "no credential in the live store"
	wantOwnerAppeared = "refresh discarded: an owner appeared mid-refresh"
	wantLiveChanged   = "refresh discarded: live credential changed mid-refresh"
)

func usageOK(_ context.Context, _ string) (map[string]any, error) {
	return map[string]any{"five_hour": map[string]any{"utilization": 10.0}}, nil
}

func expiredByClock(s *store.Store) string {
	return oauthCreds("old-access", "old-refresh", s.Clk.Now().UnixMilli()-1000)
}

// refreshingStore seeds a no-owner active slot whose live store and backup hold
// creds (expired by wall time, so oauth refreshes it); onRefresh runs inside
// the refresh before the rotated credential is returned.
func refreshingStore(t *testing.T, onRefresh func(s *store.Store)) (*store.Store, string) {
	t.Helper()
	realNowMS := time.Now().UnixMilli()
	rotated := oauthCreds("new-access", "new-refresh", realNowMS+3600_000)
	var s *store.Store
	oc := &oauth.FakeClient{
		RefreshFn: func(_ context.Context, _ string) oauth.RefreshOutcome {
			if onRefresh != nil {
				onRefresh(s)
			}
			return oauth.RefreshOutcome{Credentials: rotated}
		},
		UsageFn: usageOK,
	}
	s = newStore(t, nil, oc)
	creds := oauthCreds("old-access", "old-refresh", realNowMS-3600_000)
	if err := s.Creds.WriteBackup("1", skipEmail, creds); err != nil {
		t.Fatal(err)
	}
	writeActiveCreds(t, s, creds)
	return s, creds
}

func assertSkip(t *testing.T, rec usage.FetchRecord, want string) {
	t.Helper()
	if rec.Sentinel != jsonout.UsageTokenExpired && rec.Sentinel != jsonout.UsageNoCredentials {
		t.Errorf("sentinel = %q, want a skip sentinel", rec.Sentinel)
	}
	if rec.SkipReason != want {
		t.Errorf("SkipReason = %q, want %q", rec.SkipReason, want)
	}
}

func TestFetchActiveUsageSkipReason_OwnerExpired(t *testing.T) {
	s := newStore(t, nil, &oauth.FakeClient{UsageFn: usageOK})
	makeLiveSession(t, s, "1", skipEmail)
	assertSkip(t, fetchActiveUsage(s, "1", skipEmail, expiredByClock(s)), wantOwnerExpired)
}

func TestFetchActiveUsageSkipReason_BackupMismatch(t *testing.T) {
	s := newStore(t, nil, &oauth.FakeClient{UsageFn: usageOK})
	if err := s.Creds.WriteBackup("1", skipEmail, oauthCreds("other-access", "other-refresh", 0)); err != nil {
		t.Fatal(err)
	}
	assertSkip(t, fetchActiveUsage(s, "1", skipEmail, expiredByClock(s)),
		"live credential does not match slot 1's backup; refresh skipped")
}

func TestFetchActiveUsageSkipReason_NoCredential(t *testing.T) {
	s := newStore(t, nil, &oauth.FakeClient{UsageFn: usageOK})
	assertSkip(t, fetchActiveUsage(s, "1", skipEmail, ""), wantNoCredential)
}

func TestFetchActiveUsageSkipReason_OwnerAppeared(t *testing.T) {
	s, creds := refreshingStore(t, func(s *store.Store) { makeLiveSession(t, s, "1", skipEmail) })
	assertSkip(t, fetchActiveUsage(s, "1", skipEmail, creds), wantOwnerAppeared)
}

func TestFetchActiveUsageSkipReason_LiveChanged(t *testing.T) {
	s, creds := refreshingStore(t, func(s *store.Store) {
		writeActiveCreds(t, s, oauthCreds("other-access", "other-refresh", 0))
	})
	assertSkip(t, fetchActiveUsage(s, "1", skipEmail, creds), wantLiveChanged)
}

type failActiveWrite struct{ credstore.Store }

func (failActiveWrite) WriteActive(string) error { return errors.New("disk full") }

func TestFetchActiveUsageSkipReason_WriteError(t *testing.T) {
	s, creds := refreshingStore(t, nil)
	s.Creds = failActiveWrite{s.Creds}
	assertSkip(t, fetchActiveUsage(s, "1", skipEmail, creds), "refresh discarded: disk full")
}

func TestFetchActiveUsageSkipReason_LockContention(t *testing.T) {
	s, creds := refreshingStore(t, nil)
	s.Lock = filelock.New(s.LockFile, 20*time.Millisecond)
	holder := filelock.New(s.LockFile, time.Second)
	if ok, err := holder.Acquire(time.Second); err != nil || !ok {
		t.Fatalf("holder.Acquire = (%v, %v)", ok, err)
	}
	t.Cleanup(func() { _ = holder.Release() })
	assertSkip(t, fetchActiveUsage(s, "1", skipEmail, creds),
		"refresh discarded: Failed to acquire lock - another instance may be running")
}

func TestFetchActiveUsageSkipReason_RefreshFailed(t *testing.T) {
	oc := &oauth.FakeClient{
		RefreshFn: func(_ context.Context, _ string) oauth.RefreshOutcome {
			return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
		},
		UsageFn: usageOK,
	}
	s := newStore(t, nil, oc)
	expiresMS := s.Clk.Now().UnixMilli()
	if real := time.Now().UnixMilli(); real < expiresMS {
		expiresMS = real
	}
	creds := oauthCreds("old-access", "old-refresh", expiresMS-3600_000)
	if err := s.Creds.WriteBackup("1", skipEmail, creds); err != nil {
		t.Fatal(err)
	}
	writeActiveCreds(t, s, creds)
	assertSkip(t, fetchActiveUsage(s, "1", skipEmail, creds), "expired token could not be refreshed: invalid_grant")
}

func TestFetchActiveUsageSkipReason_SuccessHasNone(t *testing.T) {
	s, creds := refreshingStore(t, nil)
	rec := fetchActiveUsage(s, "1", skipEmail, creds)
	if rec.Sentinel != "" || rec.SkipReason != "" || rec.Usage == nil {
		t.Errorf("a kept refresh must fetch without a skip, got %+v", rec)
	}
}

func TestStaticSentinelNoCredentialsCarriesReason(t *testing.T) {
	s := newStore(t, nil, nil)
	makeLiveSession(t, s, "1", skipEmail)
	cases := []struct {
		name           string
		info           AccountInfo
		sentinel, want string
	}{
		{"active without credential", AccountInfo{Number: 2, Email: "b@example.com", IsActive: true}, jsonout.UsageNoCredentials, wantNoCredential},
		{"active keychain unavailable", AccountInfo{Number: 2, Email: "b@example.com", IsActive: true, KeychainUnavailable: true},
			jsonout.UsageKeychainUnavailable, jsonout.UsageKeychainUnavailable},
		{"active owner expired", AccountInfo{Number: 1, Email: skipEmail, IsActive: true, Creds: expiredByClock(s)},
			jsonout.UsageTokenExpired, wantOwnerExpired},
		{"inactive without credential", AccountInfo{Number: 2, Email: "b@example.com"}, jsonout.UsageNoCredentials, ""},
		{"api key", AccountInfo{Number: 3, Email: "k@example.com", IsActive: true, Creds: "sk-ant-api03-XXXX"}, jsonout.UsageAPIKey, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sentinel, reason := staticUsageSentinel(s, tc.info)
			if sentinel != tc.sentinel || reason != tc.want {
				t.Errorf("staticUsageSentinel = (%q, %q), want (%q, %q)", sentinel, reason, tc.sentinel, tc.want)
			}
		})
	}
}

func usageRow(t *testing.T, s *store.Store, num string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(s.BackupDir(), "cache", "usage.json"))
	if err != nil {
		return nil
	}
	var top struct {
		Accounts map[string]map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	return top.Accounts[num]
}

func logCount(t *testing.T, s *store.Store, needle string) int {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log"))
	return strings.Count(string(b), needle)
}

func TestCollectSkipReason_RecordedLoggedOnceAndCleared(t *testing.T) {
	calls := 0
	s := newStore(t, nil, recordingUsage(&calls, map[string]any{"five_hour": map[string]any{"utilization": 10.0}}))
	makeLiveSession(t, s, "1", skipEmail)
	expired := []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true, Creds: expiredByClock(s)}}

	CollectUsageEntries(s, expired, nil)
	entries := CollectUsageEntries(s, expired, nil)
	if calls != 0 {
		t.Fatalf("owned+expired fetched %d times, want 0", calls)
	}
	if got := entries["1"].SkipReason; got != wantOwnerExpired {
		t.Errorf("returned SkipReason = %q, want %q", got, wantOwnerExpired)
	}
	if row := usageRow(t, s, "1"); row == nil || row["skipReason"] != wantOwnerExpired {
		t.Errorf("row = %v, want skipReason %q", row, wantOwnerExpired)
	}
	if n := logCount(t, s, "Usage for account 1 not fetched: "+wantOwnerExpired); n != 1 {
		t.Errorf("not-fetched warning logged %d times over two passes, want 1", n)
	}

	if err := os.RemoveAll(sessprofile.SessionDirFor(s.BackupDir(), "1", skipEmail)); err != nil {
		t.Fatal(err)
	}
	valid := oauthCreds("at", "rt", 0)
	if err := s.Creds.WriteBackup("1", skipEmail, valid); err != nil {
		t.Fatal(err)
	}
	CollectUsageEntries(s, []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true, Creds: valid}}, nil)
	if calls != 1 {
		t.Fatalf("recovered account fetched %d times, want 1", calls)
	}
	row := usageRow(t, s, "1")
	if _, present := row["skipReason"]; present {
		t.Errorf("a success must clear skipReason, row = %v", row)
	}
	if n := logCount(t, s, "Usage for account 1 fetches again"); n != 1 {
		t.Errorf("fetches-again logged %d times, want 1", n)
	}
}

func TestCollectSkipReason_FetchPathRecorded(t *testing.T) {
	calls := 0
	s := newStore(t, nil, recordingUsage(&calls, nil))
	if err := s.Creds.WriteBackup("1", skipEmail, oauthCreds("other-access", "other-refresh", 0)); err != nil {
		t.Fatal(err)
	}
	CollectUsageEntries(s, []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true, Creds: expiredByClock(s)}}, nil)
	want := "live credential does not match slot 1's backup; refresh skipped"
	if row := usageRow(t, s, "1"); row == nil || row["skipReason"] != want {
		t.Errorf("row = %v, want skipReason %q", row, want)
	}
}

func TestCollectSkipReason_DeadTokenOnActive(t *testing.T) {
	calls := 0
	s := newStore(t, nil, recordingUsage(&calls, nil))
	writeUsageRows(t, s, map[string]any{
		"1": map[string]any{"email": skipEmail, "organizationUuid": "", "authDeadStrikes": 1},
	})
	CollectUsageEntries(s, []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true, Creds: oauthCreds("at", "rt", 0)}}, nil)
	if row := usageRow(t, s, "1"); row["skipReason"] != jsonout.UsageReloginRequired {
		t.Errorf("row = %v, want skipReason %q", row, jsonout.UsageReloginRequired)
	}
}

func TestCollectSkipReason_StoreOnlyReadWritesNothing(t *testing.T) {
	calls := 0
	s := newStore(t, nil, recordingUsage(&calls, nil))
	makeLiveSession(t, s, "1", skipEmail)
	CollectUsageEntries(s, []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true, Creds: expiredByClock(s)}}, map[string]bool{})
	if row := usageRow(t, s, "1"); row != nil {
		t.Errorf("a store-only read wrote row %v", row)
	}
}
