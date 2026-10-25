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
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
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

// logCount counts tycswap.log records at level whose message is exactly msg.
func logCount(t *testing.T, s *store.Store, level logging.Level, msg string) int {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log"))
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if rec, ok := logging.ParseLine(line); ok && rec.Level == level && rec.Message == msg {
			n++
		}
	}
	return n
}

func usageFile(t *testing.T, s *store.Store) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.BackupDir(), "cache", "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCollectSkipReason_RecordedLoggedOnceAndCleared(t *testing.T) {
	calls := 0
	clk := testutil.FixedClock(t, fixedNow)
	s := newStore(t, clk, recordingUsage(&calls, map[string]any{"five_hour": map[string]any{"utilization": 10.0}}))
	makeLiveSession(t, s, "1", skipEmail)
	expired := []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true, Creds: expiredByClock(s)}}

	CollectUsageEntries(s, expired, nil)
	first := usageFile(t, s)
	clk.Advance(5 * time.Second)
	entries := CollectUsageEntries(s, expired, nil)
	if again := usageFile(t, s); again != first {
		t.Errorf("an unchanged skip rewrote usage.json:\n%s\nwant\n%s", again, first)
	}
	if calls != 0 {
		t.Fatalf("owned+expired fetched %d times, want 0", calls)
	}
	if got := entries["1"].SkipReason; got != wantOwnerExpired {
		t.Errorf("returned SkipReason = %q, want %q", got, wantOwnerExpired)
	}
	if row := usageRow(t, s, "1"); row == nil || row["skipReason"] != wantOwnerExpired {
		t.Errorf("row = %v, want skipReason %q", row, wantOwnerExpired)
	}
	if n := logCount(t, s, logging.LevelWarning, "Usage for account 1 not fetched: "+wantOwnerExpired); n != 1 {
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
	if n := logCount(t, s, logging.LevelInfo, "Usage for account 1 fetches again"); n != 1 {
		t.Errorf("fetches-again logged %d times at INFO, want 1", n)
	}
}

func TestCollectSkipReason_ChangedReasonWarnsAgain(t *testing.T) {
	calls := 0
	s := newStore(t, nil, recordingUsage(&calls, nil))
	makeLiveSession(t, s, "1", skipEmail)
	CollectUsageEntries(s, []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true, Creds: expiredByClock(s)}}, nil)
	CollectUsageEntries(s, []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true}}, nil)
	for _, reason := range []string{wantOwnerExpired, wantNoCredential} {
		if n := logCount(t, s, logging.LevelWarning, "Usage for account 1 not fetched: "+reason); n != 1 {
			t.Errorf("WARNING for %q logged %d times, want 1", reason, n)
		}
	}
	if row := usageRow(t, s, "1"); row["skipReason"] != wantNoCredential {
		t.Errorf("row = %v, want the changed reason %q", row, wantNoCredential)
	}
}

func TestCollectSkipReason_KeptRefreshFailedGetIsPlainFailure(t *testing.T) {
	realNowMS := time.Now().UnixMilli()
	oc := &oauth.FakeClient{
		RefreshFn: func(_ context.Context, _ string) oauth.RefreshOutcome {
			return oauth.RefreshOutcome{Credentials: oauthCreds("new-access", "new-refresh", realNowMS+3600_000)}
		},
		UsageFn: func(_ context.Context, _ string) (map[string]any, error) {
			return nil, &oauth.HTTPError{Code: 429}
		},
	}
	s := newStore(t, nil, oc)
	expiresMS := s.Clk.Now().UnixMilli()
	if realNowMS < expiresMS {
		expiresMS = realNowMS
	}
	creds := oauthCreds("old-access", "old-refresh", expiresMS-3600_000)
	if err := s.Creds.WriteBackup("1", skipEmail, creds); err != nil {
		t.Fatal(err)
	}
	writeActiveCreds(t, s, creds)
	CollectUsageEntries(s, []AccountInfo{{Number: 1, Email: skipEmail, IsActive: true, Creds: creds}}, nil)
	row := usageRow(t, s, "1")
	if _, present := row["skipReason"]; present {
		t.Errorf("a kept refresh with a failed GET wrote a skip reason, row = %v", row)
	}
	if row["lastError"] != "http-429" {
		t.Errorf("lastError = %v, want the GET's http-429 recorded as a plain failure", row["lastError"])
	}
}

// groupOwnedStore makes Account-2 owned by the opus group profile, whose
// .credentials.json holds profileCreds ("" leaves it absent).
func groupOwnedStore(t *testing.T, profileCreds string) *store.Store {
	t.Helper()
	s := newStore(t, nil, &oauth.FakeClient{UsageFn: usageOK})
	writeSequenceRaw(t, s, `{"activeAccountNumber":2,"sequence":[2],"accounts":{"2":{"email":"group@example.test","organizationUuid":""}}}`)
	account := groups.Account{Number: "2", Email: "group@example.test"}
	dir := groups.ProfileDir(s.BackupDir(), groups.Opus)
	for path, value := range map[string]any{
		groups.ActivePath(s.BackupDir(), groups.Opus): account,
		filepath.Join(dir, ".claude.json"):            map[string]any{"oauthAccount": map[string]any{"emailAddress": account.Email, "organizationUuid": ""}},
	} {
		if err := groups.WriteJSON(path, value); err != nil {
			t.Fatal(err)
		}
	}
	registry := groups.Registry{Version: 1, Claims: map[string]groups.Owner{"2": {Scope: "opus", ProfileDir: dir, Account: account}}}
	if err := registry.Save(s.BackupDir()); err != nil {
		t.Fatal(err)
	}
	if profileCreds != "" {
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(profileCreds), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func groupInfo(active bool) AccountInfo {
	return AccountInfo{Number: 2, Email: "group@example.test", IsActive: active, Creds: oauthCreds("at", "rt", 0)}
}

func TestFetchAccountUsageSkipReason_OwnerError(t *testing.T) {
	s := groupOwnedStore(t, oauthCreds("group-access", "group-refresh", 0))
	if err := os.WriteFile(groups.ClaimsPath(s.BackupDir()), []byte(`{"version": 2, "claims": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ownerErr := s.CredentialOwner("2")
	if ownerErr == nil {
		t.Fatal("fixture: a bad registry must make the owner lookup fail")
	}
	rec := fetchAccountUsage(s, groupInfo(true))
	if want := "credential owner unknown: " + ownerErr.Error(); rec.Sentinel != jsonout.UsageNoCredentials || rec.SkipReason != want {
		t.Errorf("record = %q/%q, want %q/%q", rec.Sentinel, rec.SkipReason, jsonout.UsageNoCredentials, want)
	}
}

func TestFetchAccountUsageSkipReason_OwnerUncertain(t *testing.T) {
	s := groupOwnedStore(t, "")
	if owner, err := s.CredentialOwner("2"); err != nil || !owner.Uncertain {
		t.Fatalf("fixture: owner = %+v, %v; want an uncertain owner", owner, err)
	}
	rec := fetchAccountUsage(s, groupInfo(true))
	if want := "credential owner unknown: Account-2 credential ownership is uncertain"; rec.SkipReason != want {
		t.Errorf("SkipReason = %q, want %q", rec.SkipReason, want)
	}
}

func TestFetchAccountUsageSkipReason_OwnedStoreNoToken(t *testing.T) {
	s := groupOwnedStore(t, `{"claudeAiOauth": {"refreshToken": "group-refresh"}}`)
	assertSkip(t, fetchAccountUsage(s, groupInfo(true)), wantNoCredential)
}

func TestFetchAccountUsageSkipReason_OwnedStoreExpired(t *testing.T) {
	s := groupOwnedStore(t, "")
	dir := groups.ProfileDir(s.BackupDir(), groups.Opus)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(expiredByClock(s)), 0o600); err != nil {
		t.Fatal(err)
	}
	assertSkip(t, fetchAccountUsage(s, groupInfo(true)), wantOwnerExpired)
	if rec := fetchAccountUsage(s, groupInfo(false)); rec.Sentinel != jsonout.UsageTokenExpired || rec.SkipReason != "" {
		t.Errorf("inactive owned+expired = %q/%q, want the sentinel without a reason", rec.Sentinel, rec.SkipReason)
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
