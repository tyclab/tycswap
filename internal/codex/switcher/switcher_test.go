// switcher_test.go — the verbs, and the two rules that make them correct.
// Ports claude-swap PR #252 tests/test_codex_switcher.py assertion for
// assertion, plus the edge cases the Go surface adds (renumbering, rotation,
// status JSON, token diagnostics, BindingPct's numeric forms).
package switcher

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/testutil"
)

const (
	acctA, userA = "acct-a", "user-a"
	acctB, userB = "acct-b", "user-b"
	t0           = "2026-09-30T12:00:00Z"
)

var (
	keyA = authfile.AccountKey(userA, acctA)
	keyB = authfile.AccountKey(userB, acctB)
	ctx  = context.Background()
)

type fixture struct {
	t         *testing.T
	codexHome string
	root      string
	clk       *clock.Fake
	kc        *keychain.Fake
	out       *bytes.Buffer
	stdin     string
	pids      []int

	mu         sync.Mutex
	usageCalls atomic.Int32
	usageFn    func(accountID string) api.UsageFetch
	refreshFn  func(payload map[string]any) api.RefreshOutcome
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	testutil.Setenv(t, "HOME", home)
	codexHome := filepath.Join(home, ".codex")
	testutil.Setenv(t, "CODEX_HOME", codexHome)
	testutil.Setenv(t, "XDG_DATA_HOME", filepath.Join(home, "xdg"))
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t: t, codexHome: codexHome, root: filepath.Join(home, "store"),
		clk: testutil.FixedClock(t, t0), kc: keychain.NewFake(), out: &bytes.Buffer{},
	}
	f.usageFn = func(string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 1.0}}}
	}
	return f
}

func (f *fixture) client() *api.FakeClient {
	return &api.FakeClient{
		UsageFn: func(_ context.Context, _, accountID string) api.UsageFetch {
			f.usageCalls.Add(1)
			return f.usageFn(accountID)
		},
		RefreshFn: func(_ context.Context, p map[string]any) api.RefreshOutcome {
			if f.refreshFn != nil {
				return f.refreshFn(p)
			}
			return api.RefreshOutcome{Kind: api.KindTransient}
		},
	}
}

// open is a fresh Switcher over the fixture's state, as Python's repeated
// CodexSwitcher() calls are.
func (f *fixture) open() *Switcher {
	return New(Options{
		Root: f.root, Keychain: f.kc, Platform: platform.Linux, Clock: f.clk,
		Client: f.client(), Stdout: f.out, Stdin: strings.NewReader(f.stdin),
		RunningPIDs: func() []int { return f.pids }, LockTimeout: 100 * time.Millisecond,
	})
}

func (f *fixture) now() float64 { return clock.Seconds(f.clk) }

func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	seg := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return seg(map[string]any{"alg": "none", "typ": "JWT"}) + "." + seg(claims) + ".sig"
}

// authJSON builds an auth.json payload in the shape codex writes
// (conftest_codex.py make_auth_json).
func (f *fixture) authJSON(acct, user, email, refreshToken string) map[string]any {
	token := makeJWT(f.t, map[string]any{
		"exp":   int64(f.now()) + 3600,
		"email": email,
		authfile.AuthClaim: map[string]any{
			"chatgpt_account_id": acct, "chatgpt_user_id": user, "chatgpt_plan_type": "pro",
		},
	})
	return map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": token, "access_token": token, "refresh_token": refreshToken, "account_id": acct,
		},
		"last_refresh": "2026-08-16T00:00:00Z",
	}
}

func (f *fixture) writeLive(payload any) {
	f.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.codexHome, "auth.json"), raw, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) readLive() map[string]any {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.codexHome, "auth.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

// seeded is two managed accounts, A live.
func (f *fixture) seeded() *Switcher {
	f.t.Helper()
	sw := f.open()
	st := sw.Store()
	for _, r := range []struct{ key, acct, user, email string }{
		{keyA, acctA, userA, "a@x"}, {keyB, acctB, userB, "b@x"},
	} {
		if _, err := st.UpsertSlot(r.key, store.Upsert{Email: r.email, Plan: "pro"}); err != nil {
			f.t.Fatal(err)
		}
		if err := st.WriteSnapshot(r.key, f.authJSON(r.acct, r.user, r.email, "rt-a")); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := st.SetActive(keyA); err != nil {
		f.t.Fatal(err)
	}
	f.writeLive(f.authJSON(acctA, userA, "a@x", "rt-a"))
	return sw
}

// holdLock takes the store lock as "another process" would.
func holdLock(t *testing.T, sw *Switcher) {
	t.Helper()
	l := sw.Store().Lock()
	ok, err := l.Acquire(time.Second)
	if err != nil || !ok {
		t.Fatalf("could not take the store lock: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() { _ = l.Release() })
}

func tokensOf(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	tok, ok := payload["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("payload has no tokens: %v", payload)
	}
	return tok
}

func wantErr(t *testing.T, err error, kind cerr.Kind, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s error containing %q, got nil", kind, substr)
	}
	if got := cerr.TypeName(err); got != string(kind) {
		t.Errorf("kind = %q, want %q (%v)", got, kind, err)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Errorf("error %q does not contain %q", err, substr)
	}
}

// ---- the seam ----------------------------------------------------------------

func TestProviderIDIsCodex(t *testing.T) {
	if ProviderID != "codex" {
		t.Fatalf("ProviderID = %q", ProviderID)
	}
}

// ---- rule 1: the live file decides who is active ------------------------------

func TestActiveAccountIsDerivedFromTheLiveFile(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	_ = sw.Store().SetActive(keyB) // registry says B, live file still holds A
	if got := sw.CurrentAccountNumber(); got != "1" {
		t.Fatalf("CurrentAccountNumber = %q, want 1", got)
	}
}

func TestActiveIsEmptyWhenTheLiveLoginIsUnmanaged(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	f.writeLive(f.authJSON("stranger", "user-z", "z@x", "rt"))
	if got := sw.CurrentAccountNumber(); got != "" {
		t.Fatalf("CurrentAccountNumber = %q, want empty", got)
	}
}

func TestActiveIsEmptyWhenThereIsNoLiveLogin(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	_ = os.Remove(filepath.Join(f.codexHome, "auth.json"))
	if got := sw.CurrentAccountNumber(); got != "" {
		t.Fatalf("CurrentAccountNumber = %q, want empty", got)
	}
}

func TestSwitchCapturesTheOutgoingLoginIntoItsOwnSlot(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	_ = sw.Store().SetActive(keyB) // registry is wrong; live file is A
	f.writeLive(f.authJSON(acctA, userA, "a@x", "rt-rotated"))

	if _, err := sw.SwitchTo(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	st := f.open().Store()
	if got := tokensOf(t, st.ReadSnapshot(keyA))["refresh_token"]; got != "rt-rotated" {
		t.Errorf("A's snapshot refresh_token = %v, want rt-rotated", got)
	}
	// ...and B's own snapshot was not overwritten with A's tokens.
	if got := tokensOf(t, st.ReadSnapshot(keyB))["account_id"]; got != acctB {
		t.Errorf("B's snapshot account_id = %v, want %s", got, acctB)
	}
}

func TestAnUnmanagedLiveLoginIsNotCapturedIntoAnySlot(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	before := sw.Store().ReadSnapshot(keyA)
	f.writeLive(f.authJSON("stranger", "user-z", "z@x", "rt"))
	if _, err := sw.SwitchTo(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	if after := sw.Store().ReadSnapshot(keyA); !reflect.DeepEqual(after, before) {
		t.Fatalf("A's snapshot changed:\n before %v\n after  %v", before, after)
	}
}

// ---- switching ---------------------------------------------------------------

func TestSwitchWritesTheTargetSnapshotToTheLiveFile(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	res, err := sw.SwitchTo(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if got := tokensOf(t, f.readLive())["account_id"]; got != acctB {
		t.Errorf("live account_id = %v, want %s", got, acctB)
	}
	if res.Number != "2" || res.Email != "b@x" {
		t.Errorf("result = %+v", res)
	}
	if sw.CurrentAccountNumber() != "2" {
		t.Errorf("CurrentAccountNumber = %q after switch", sw.CurrentAccountNumber())
	}
}

func TestSwitchRecordsTheNewActiveSlot(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	if _, err := sw.SwitchTo(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	if got := f.open().Store().ActiveKey(); got != keyB {
		t.Fatalf("ActiveKey = %q, want %q", got, keyB)
	}
}

func TestSwitchReportsRunningCodexSessions(t *testing.T) {
	f := newFixture(t)
	f.pids = []int{4242}
	sw := f.seeded()
	res, err := sw.SwitchTo(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.RunningPIDs, []int{4242}) {
		t.Fatalf("RunningPIDs = %v", res.RunningPIDs)
	}
}

func TestSwitchWithNothingRunningReportsAnEmptyList(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	res, err := sw.SwitchTo(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if res.RunningPIDs == nil || len(res.RunningPIDs) != 0 {
		t.Fatalf("RunningPIDs = %#v, want empty non-nil", res.RunningPIDs)
	}
}

func TestSwitchToAnUnknownAccountFails(t *testing.T) {
	f := newFixture(t)
	_, err := f.seeded().SwitchTo(ctx, "99")
	wantErr(t, err, cerr.KindAccountNotFound, "No Codex account matches '99'")
}

func TestSwitchToAnAccountWithoutCredentialsFails(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	_ = sw.Store().DeleteSnapshot(keyB)
	_, err := sw.SwitchTo(ctx, "2")
	wantErr(t, err, cerr.KindSwitch, "Codex account 2 has no stored credentials")
}

func TestSwitchRollsBackWhenWritingTheLiveFileFails(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	before := f.readLive()
	calls := 0
	sw.writeLive = func(p map[string]any) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("disk full")
		}
		return authfile.WriteLiveAuth(p) // the rollback write is allowed through
	}
	_, err := sw.SwitchTo(ctx, "2")
	wantErr(t, err, cerr.KindSwitch, "Failed to activate Codex account: disk full")
	if calls != 2 {
		t.Errorf("writeLive calls = %d, want 2 (write + rollback)", calls)
	}
	if after := f.readLive(); !reflect.DeepEqual(after, before) {
		t.Errorf("live file not restored")
	}
	if got := sw.Store().ActiveKey(); got != keyA {
		t.Errorf("ActiveKey = %q, want %q", got, keyA)
	}
}

func TestSwitchIsSerializedByTheStoreLock(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	holdLock(t, sw)
	_, err := sw.SwitchTo(ctx, "2")
	wantErr(t, err, cerr.KindLock, "Another tycswap process")
	if got := tokensOf(t, f.readLive())["account_id"]; got != acctA {
		t.Errorf("live file changed under a held lock: %v", got)
	}
}

// ---- rule 2: never refresh the active account from its snapshot ---------------

func alwaysDue(any, float64) bool { return true }

func TestTheActiveAccountIsNeverRefreshedFromItsSnapshot(t *testing.T) {
	f := newFixture(t)
	var refreshed []any
	f.refreshFn = func(p map[string]any) api.RefreshOutcome {
		f.mu.Lock()
		refreshed = append(refreshed, p["tokens"].(map[string]any)["account_id"])
		f.mu.Unlock()
		return api.RefreshOutcome{Payload: p}
	}
	sw := f.seeded()
	sw.needsRefresh = alwaysDue
	sw.AccountsSnapshot(ctx, map[string]bool{"1": true, "2": true})

	has := func(v string) bool {
		for _, r := range refreshed {
			if r == v {
				return true
			}
		}
		return false
	}
	if has(acctA) {
		t.Errorf("the active account was refreshed: %v", refreshed)
	}
	if !has(acctB) {
		t.Errorf("the inactive account was not refreshed: %v", refreshed)
	}
}

func TestAFreshTokenIsNotRefreshedAtAll(t *testing.T) {
	f := newFixture(t)
	n := 0
	f.refreshFn = func(p map[string]any) api.RefreshOutcome { n++; return api.RefreshOutcome{Payload: p} }
	sw := f.seeded()
	sw.needsRefresh = func(any, float64) bool { return false }
	sw.AccountsSnapshot(ctx, map[string]bool{"2": true})
	if n != 0 {
		t.Fatalf("refreshed %d times, want 0", n)
	}
}

func TestTheDefaultExpiryCheckLeavesAFreshTokenAlone(t *testing.T) {
	f := newFixture(t)
	n := 0
	f.refreshFn = func(p map[string]any) api.RefreshOutcome { n++; return api.RefreshOutcome{Payload: p} }
	f.seeded().AccountsSnapshot(ctx, map[string]bool{"2": true})
	if n != 0 {
		t.Fatalf("a token valid for an hour was refreshed %d times", n)
	}
}

func TestARotatedRefreshTokenIsPersistedImmediately(t *testing.T) {
	f := newFixture(t)
	f.refreshFn = func(p map[string]any) api.RefreshOutcome {
		raw, _ := json.Marshal(p)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		out["tokens"].(map[string]any)["refresh_token"] = "rt-rotated"
		return api.RefreshOutcome{Payload: out}
	}
	sw := f.seeded()
	sw.needsRefresh = alwaysDue
	sw.AccountsSnapshot(ctx, map[string]bool{"2": true})
	if got := tokensOf(t, f.open().Store().ReadSnapshot(keyB))["refresh_token"]; got != "rt-rotated" {
		t.Fatalf("B's refresh_token = %v, want rt-rotated", got)
	}
}

// The pre-lock refresh decision is only a hint. Another process may refresh
// the slot while this one waits for the lock; what is sent must be what the
// store holds once the lock is ours, or nothing at all.
func TestTheRefreshDecisionIsRemadeUnderTheLock(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stillDue    bool
		wantSent    []string
		wantPayload string
	}{
		{"fresh snapshot still due sends the fresh token", true, []string{"rt-fresh"}, "rt-fresh"},
		{"fresh snapshot no longer due is not refreshed", false, nil, "rt-fresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			var sent []string
			f.refreshFn = func(p map[string]any) api.RefreshOutcome {
				f.mu.Lock()
				sent = append(sent, p["tokens"].(map[string]any)["refresh_token"].(string))
				f.mu.Unlock()
				return api.RefreshOutcome{Kind: api.KindTransient}
			}
			sw := f.seeded()
			calls := 0
			sw.needsRefresh = func(any, float64) bool {
				calls++
				if calls == 1 {
					// "Another process" refreshes B between the pre-check
					// and the lock.
					if err := f.open().Store().WriteSnapshot(keyB, f.authJSON(acctB, userB, "b@x", "rt-fresh")); err != nil {
						t.Fatal(err)
					}
					return true
				}
				return tc.stillDue
			}
			slotB := *sw.Store().SlotForKey(keyB)
			got := sw.payloadForUsage(ctx, slotB, sw.CurrentAccountNumber())
			if !reflect.DeepEqual(sent, tc.wantSent) {
				t.Errorf("refresh tokens sent = %v, want %v", sent, tc.wantSent)
			}
			if rt := tokensOf(t, got)["refresh_token"]; rt != tc.wantPayload {
				t.Errorf("served refresh_token = %v, want %s", rt, tc.wantPayload)
			}
		})
	}
}

// A slot another terminal made active while this one waited for the lock is
// now codex's to refresh: it must be read from the live file, not refreshed.
func TestASlotThatBecameActiveBeforeTheLockIsNotRefreshed(t *testing.T) {
	f := newFixture(t)
	n := 0
	f.refreshFn = func(p map[string]any) api.RefreshOutcome { n++; return api.RefreshOutcome{Payload: p} }
	sw := f.seeded()
	active := sw.CurrentAccountNumber()
	sw.needsRefresh = func(any, float64) bool {
		f.writeLive(f.authJSON(acctB, userB, "b@x", "rt-live-b"))
		return true
	}
	got := sw.payloadForUsage(ctx, *sw.Store().SlotForKey(keyB), active)
	if n != 0 {
		t.Fatalf("refreshed %d times, want 0", n)
	}
	if rt := tokensOf(t, got)["refresh_token"]; rt != "rt-live-b" {
		t.Fatalf("served refresh_token = %v, want the live file's rt-live-b", rt)
	}
}

// Switching to the account that is already live captures its fresh tokens and
// leaves auth.json byte for byte as codex wrote it.
func TestSwitchingToTheActiveAccountLeavesTheLiveFileAlone(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	f.writeLive(f.authJSON(acctA, userA, "a@x", "rt-rotated-by-codex"))
	livePath := filepath.Join(f.codexHome, "auth.json")
	before, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	sw.writeLive = func(p map[string]any) (string, error) { writes++; return authfile.WriteLiveAuth(p) }

	res, err := sw.SwitchTo(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || writes != 0 {
		t.Errorf("auth.json rewritten (%d writes):\n before %s\n after  %s", writes, before, after)
	}
	if got := tokensOf(t, f.open().Store().ReadSnapshot(keyA))["refresh_token"]; got != "rt-rotated-by-codex" {
		t.Errorf("A's snapshot refresh_token = %v, want rt-rotated-by-codex", got)
	}
	if !res.AlreadyActive || res.Number != "1" || len(res.RunningPIDs) != 0 {
		t.Errorf("result = %+v, want AlreadyActive for slot 1 and no sessions to restart", res)
	}
}

func TestABusyStoreSkipsTheRefreshRatherThanLosingTheToken(t *testing.T) {
	f := newFixture(t)
	n := 0
	f.refreshFn = func(p map[string]any) api.RefreshOutcome { n++; return api.RefreshOutcome{Payload: p} }
	sw := f.seeded()
	sw.needsRefresh = alwaysDue
	holdLock(t, sw)
	sw.AccountsSnapshot(ctx, map[string]bool{"2": true})
	if n != 0 {
		t.Fatalf("refreshed %d times under a held lock, want 0", n)
	}
}

func TestAFailedRefreshStillServesTheStoredPayload(t *testing.T) {
	f := newFixture(t)
	f.refreshFn = func(map[string]any) api.RefreshOutcome { return api.RefreshOutcome{Kind: api.KindTransient} }
	sw := f.seeded()
	sw.needsRefresh = alwaysDue
	snap := sw.AccountsSnapshot(ctx, map[string]bool{"2": true})
	if got := snap.Accounts[1].Usage.Sentinel; got != "" {
		t.Fatalf("sentinel = %q, want none (served, not blanked)", got)
	}
	if f.usageCalls.Load() != 1 {
		t.Errorf("usage calls = %d, want 1", f.usageCalls.Load())
	}
}

// ---- the read model ----------------------------------------------------------

func TestSnapshotTagsEveryRowWithTheCodexProvider(t *testing.T) {
	f := newFixture(t)
	snap := f.seeded().AccountsSnapshot(ctx, nil)
	if snap.Provider != reporting.ProviderCodex {
		t.Errorf("snapshot provider = %q", snap.Provider)
	}
	if len(snap.Accounts) != 2 {
		t.Fatalf("rows = %d", len(snap.Accounts))
	}
	for _, a := range snap.Accounts {
		if a.Provider != "codex" || a.Key() != "codex:"+a.Number {
			t.Errorf("row %s provider=%q key=%q", a.Number, a.Provider, a.Key())
		}
	}
	if snap.ActiveNumber != "1" || !snap.Accounts[0].IsActive || snap.Accounts[1].IsActive {
		t.Errorf("active flags wrong: %+v", snap)
	}
	if snap.TakenAt != f.now() {
		t.Errorf("TakenAt = %v, want the clock's %v", snap.TakenAt, f.now())
	}
	if !snap.Accounts[1].RotationEligible || !snap.Accounts[1].Switchable || snap.Accounts[1].Kind != "oauth" {
		t.Errorf("row 2 eligibility wrong: %+v", snap.Accounts[1])
	}
}

func TestAnEmptyFetchSetMakesNoRequests(t *testing.T) {
	f := newFixture(t)
	f.usageFn = func(string) api.UsageFetch { t.Error("no request should be made"); return api.UsageFetch{} }
	if n := len(f.seeded().AccountsSnapshot(ctx, map[string]bool{}).Accounts); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

func TestFetchNilMakesEveryAccountEligible(t *testing.T) {
	f := newFixture(t)
	f.seeded().AccountsSnapshot(ctx, nil)
	if got := f.usageCalls.Load(); got != 2 {
		t.Fatalf("usage calls = %d, want 2", got)
	}
}

func TestAFetchSetRestrictsWhichAccountsAreFetched(t *testing.T) {
	f := newFixture(t)
	f.seeded().AccountsSnapshot(ctx, map[string]bool{"2": true})
	if got := f.usageCalls.Load(); got != 1 {
		t.Fatalf("usage calls = %d, want 1", got)
	}
}

func TestAUsageFailureBecomesASentinelNotAnError(t *testing.T) {
	f := newFixture(t)
	f.usageFn = func(string) api.UsageFetch { return api.UsageFetch{Sentinel: "http 401"} }
	snap := f.seeded().AccountsSnapshot(ctx, map[string]bool{"1": true})
	if got := snap.Accounts[0].Usage.Sentinel; got != "http 401" {
		t.Fatalf("sentinel = %q, want http 401", got)
	}
}

func TestASlotWhoseSnapshotIsGoneReportsNoCredentials(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	_ = sw.Store().DeleteSnapshot(keyB)
	snap := sw.AccountsSnapshot(ctx, map[string]bool{"2": true})
	if got := snap.Accounts[1].Usage.Sentinel; got != "no credentials" {
		t.Fatalf("sentinel = %q, want no credentials", got)
	}
}

func TestAnAPIKeyAccountRendersASentinelAndIsNotSwitchable(t *testing.T) {
	f := newFixture(t)
	sw := f.open()
	st := sw.Store()
	if _, err := st.UpsertSlot(keyA, store.Upsert{Email: "a@x", AuthMode: "apikey"}); err != nil {
		t.Fatal(err)
	}
	_ = st.WriteSnapshot(keyA, map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x", "tokens": nil})
	row := sw.AccountsSnapshot(ctx, nil).Accounts[0]
	if row.Usage.Sentinel != "api key" || row.Kind != "api_key" || row.Switchable || row.RotationEligible {
		t.Errorf("row = %+v", row)
	}
	if got := sw.SwitchableAccountNumbers(); len(got) != 0 {
		t.Errorf("SwitchableAccountNumbers = %v, want none", got)
	}
	if f.usageCalls.Load() != 0 {
		t.Errorf("an API-key account made %d usage requests", f.usageCalls.Load())
	}
}

func TestADisabledAccountIsNotARotationCandidate(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	_ = sw.Store().SetDisabled(keyB, true)
	if got := f.open().SwitchableAccountNumbers(); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("SwitchableAccountNumbers = %v", got)
	}
	// ...but it is still listed and still an explicit target.
	if got := f.open().AccountNumbers(); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Errorf("AccountNumbers = %v", got)
	}
	if _, err := f.open().SwitchTo(ctx, "2"); err != nil {
		t.Errorf("a disabled account is still an explicit target: %v", err)
	}
}

// ---- verbs -------------------------------------------------------------------

func TestAddCapturesTheCurrentLiveLogin(t *testing.T) {
	f := newFixture(t)
	f.writeLive(f.authJSON(acctA, userA, "a@x", "rt-a"))
	slot, err := f.open().Add(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	st := f.open().Store()
	if slot.Number != "1" || slot.Email != "a@x" {
		t.Errorf("slot = %+v", slot)
	}
	if st.ReadSnapshot(keyA) == nil {
		t.Error("no snapshot stored")
	}
	if st.ActiveKey() != keyA {
		t.Errorf("ActiveKey = %q", st.ActiveKey())
	}
}

func TestAddAcceptsAnAlias(t *testing.T) {
	f := newFixture(t)
	f.writeLive(f.authJSON(acctA, userA, "a@x", "rt-a"))
	slot, err := f.open().Add(ctx, "Work")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.open().Store().Slots()[0].Alias; got != "work" || slot.Alias != "work" {
		t.Fatalf("alias = %q / %q, want work (normalized)", got, slot.Alias)
	}
}

func TestAddRejectsAnInvalidAliasBeforeWritingAnything(t *testing.T) {
	f := newFixture(t)
	f.writeLive(f.authJSON(acctA, userA, "a@x", "rt-a"))
	_, err := f.open().Add(ctx, "42")
	wantErr(t, err, cerr.KindValidation, "purely numeric")
	if n := len(f.open().Store().Slots()); n != 0 {
		t.Fatalf("slots = %d after a rejected add", n)
	}
}

func TestAddWithoutALiveLoginFails(t *testing.T) {
	f := newFixture(t)
	_, err := f.open().Add(ctx, "")
	wantErr(t, err, cerr.KindSwitch, "No Codex login")
}

func TestAddRefusesAnUnidentifiableLogin(t *testing.T) {
	f := newFixture(t)
	f.writeLive(map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x", "tokens": nil})
	_, err := f.open().Add(ctx, "")
	wantErr(t, err, cerr.KindSwitch, "no account id")
}

func TestAddIsIdempotentForTheSameAccount(t *testing.T) {
	f := newFixture(t)
	f.writeLive(f.authJSON(acctA, userA, "a@x", "rt-a"))
	for i := 0; i < 2; i++ {
		if _, err := f.open().Add(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.open().Store().Slots()); n != 1 {
		t.Fatalf("slots = %d, want 1", n)
	}
}

func TestAddUnderAHeldLockFails(t *testing.T) {
	f := newFixture(t)
	f.writeLive(f.authJSON(acctA, userA, "a@x", "rt-a"))
	sw := f.open()
	holdLock(t, sw)
	_, err := sw.Add(ctx, "")
	wantErr(t, err, cerr.KindLock, "Another tycswap process")
}

func TestRemoveDropsTheSlotAndItsSnapshot(t *testing.T) {
	f := newFixture(t)
	removed, err := f.seeded().Remove("2", true)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	st := f.open().Store()
	if got := st.Slots(); len(got) != 1 || got[0].AccountKey != keyA {
		t.Errorf("slots = %+v", got)
	}
	if st.ReadSnapshot(keyB) != nil {
		t.Error("B's snapshot survived removal")
	}
}

func TestRemoveAsksBeforeDeleting(t *testing.T) {
	f := newFixture(t)
	f.stdin = "n\n"
	removed, err := f.seeded().Remove("2", false)
	if err != nil || removed {
		t.Fatalf("removed=%v err=%v, want a declined prompt", removed, err)
	}
	st := f.open().Store()
	if len(st.Slots()) != 2 || st.ReadSnapshot(keyB) == nil {
		t.Error("a declined prompt still removed the account")
	}
	out := f.out.String()
	if !strings.Contains(out, "Are you sure you want to permanently remove Codex account 2 (b@x)? [y/N]") ||
		!strings.Contains(out, "Cancelled") {
		t.Errorf("output = %q", out)
	}
}

func TestRemoveTreatsNoAnswerAsNo(t *testing.T) {
	f := newFixture(t)
	removed, err := f.seeded().Remove("2", false)
	if err != nil || removed || len(f.open().Store().Slots()) != 2 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}

func TestRemoveProceedsWhenThePromptIsAnsweredYes(t *testing.T) {
	f := newFixture(t)
	f.stdin = "y\n"
	removed, err := f.seeded().Remove("2", false)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if got := f.open().Store().Slots(); len(got) != 1 || got[0].AccountKey != keyA {
		t.Errorf("slots = %+v", got)
	}
	if strings.Contains(f.out.String(), "Cancelled") {
		t.Error("printed Cancelled on a confirmed removal")
	}
}

func TestRemoveWarnsWhenTheTargetIsTheActiveAccount(t *testing.T) {
	f := newFixture(t)
	f.stdin = "y\n"
	if _, err := f.seeded().Remove("1", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "Codex account 1 (a@x) is currently active") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestRemoveOfAnUnknownAccountFails(t *testing.T) {
	f := newFixture(t)
	_, err := f.seeded().Remove("99", true)
	wantErr(t, err, cerr.KindAccountNotFound, "99")
}

func TestResolveAccountAcceptsNumberEmailAndAlias(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	_ = sw.Store().SetAlias(keyB, "work")
	for _, id := range []string{"2", "b@x", "B@X", "work", "WORK", " work "} {
		num, email, label, err := f.open().ResolveAccount(id)
		if err != nil || num != "2" || email != "b@x" || label != "b@x [personal]" {
			t.Errorf("ResolveAccount(%q) = %q %q %q %v", id, num, email, label, err)
		}
	}
}

func TestResolveAccountRejectsAnEmptyIdentifier(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"", "   "} {
		if _, _, _, err := f.seeded().ResolveAccount(id); err == nil {
			t.Errorf("ResolveAccount(%q) matched", id)
		}
	}
}

func TestAliasesRoundTripThroughTheSwitcher(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	num, alias, err := sw.Alias("b@x", "Work")
	if err != nil || num != "2" || alias != "work" {
		t.Fatalf("Alias = %q %q %v", num, alias, err)
	}
	if got := sw.Store().Slots()[1].Alias; got != "work" {
		t.Errorf("alias = %q", got)
	}
	num, err = sw.UnsetAlias("work")
	if err != nil || num != "2" {
		t.Fatalf("UnsetAlias = %q %v", num, err)
	}
	if got := sw.Store().Slots()[1].Alias; got != "" {
		t.Errorf("alias = %q after unset", got)
	}
	if _, _, err := sw.Alias("2", "-x"); err == nil {
		t.Error("an invalid alias was accepted")
	}
}

func TestDisableAndEnableRoundTrip(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	for _, want := range []bool{true, false} {
		num, err := sw.SetAccountDisabled("b@x", want)
		if err != nil || num != "2" {
			t.Fatalf("SetAccountDisabled = %q %v", num, err)
		}
		if got := sw.Store().Slots()[1].Disabled; got != want {
			t.Errorf("disabled = %v, want %v", got, want)
		}
	}
}

// ---- usage caching -----------------------------------------------------------

func TestTwoConsecutiveSnapshotsCostOneRoundOfRequests(t *testing.T) {
	f := newFixture(t)
	f.usageFn = func(string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 5.0}}}
	}
	sw := f.seeded()
	both := map[string]bool{"1": true, "2": true}
	sw.AccountsSnapshot(ctx, both)
	first := f.usageCalls.Load()
	sw.AccountsSnapshot(ctx, both)
	if first != 2 {
		t.Errorf("first pass made %d requests, want 2", first)
	}
	if got := f.usageCalls.Load(); got != first {
		t.Errorf("second pass added %d requests", got-first)
	}
}

func TestACachedValueIsServedToALaterSnapshotWithoutFetching(t *testing.T) {
	f := newFixture(t)
	f.usageFn = func(string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 7.0}}}
	}
	sw := f.seeded()
	sw.AccountsSnapshot(ctx, map[string]bool{"1": true, "2": true})
	calls := f.usageCalls.Load()

	snap := f.open().AccountsSnapshot(ctx, map[string]bool{}) // pure cache read
	if f.usageCalls.Load() != calls {
		t.Error("a store-only pass made a request")
	}
	lg := snap.Accounts[0].Usage.LastGood
	if p := BindingPct(lg); p == nil || *p != 7 {
		t.Fatalf("last good = %v", lg)
	}
	if len(lg) != 1 {
		t.Errorf("last good carries extra keys: %v", lg)
	}
}

// ---- renumbering, rotation, best ---------------------------------------------

func TestSwapExchangesTwoSlotNumbers(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	a, b, err := sw.Swap("a@x", "2")
	if err != nil || a != "1" || b != "2" {
		t.Fatalf("Swap = %q %q %v", a, b, err)
	}
	if sw.Store().SlotForKey(keyA).Number != "2" || sw.Store().SlotForKey(keyB).Number != "1" {
		t.Error("numbers not exchanged")
	}
	if tokensOf(t, sw.Store().ReadSnapshot(keyA))["account_id"] != acctA {
		t.Error("renumbering moved a secret")
	}
	if _, _, err := sw.Swap("1", "b@x"); err == nil {
		t.Error("swapping an account with itself succeeded")
	}
}

func TestMoveToAFreeAndAnOccupiedNumber(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	from, to, swapped, err := sw.Move("2", "5")
	if err != nil || from != "2" || to != "5" || swapped {
		t.Fatalf("Move free = %q %q %v %v", from, to, swapped, err)
	}
	from, to, swapped, err = sw.Move("1", "5")
	if err != nil || from != "1" || to != "5" || !swapped {
		t.Fatalf("Move occupied = %q %q %v %v", from, to, swapped, err)
	}
	if sw.Store().SlotForKey(keyA).Number != "5" || sw.Store().SlotForKey(keyB).Number != "1" {
		t.Error("move did not swap the occupant")
	}
	from, to, swapped, err = sw.Move("5", "5")
	if err != nil || from != "5" || to != "5" || swapped {
		t.Errorf("Move to self = %q %q %v %v", from, to, swapped, err)
	}
}

func TestMoveRejectsAnInvalidTarget(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	for _, target := range []string{"0", "-1", "x", "", "1.5"} {
		_, _, _, err := sw.Move("1", target)
		if err == nil || !strings.Contains(err.Error(), "is not a valid slot number") {
			t.Errorf("Move(%q) err = %v", target, err)
		}
	}
}

func TestMoveUnderAHeldLockFails(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	holdLock(t, sw)
	_, _, _, err := sw.Move("1", "2")
	wantErr(t, err, cerr.KindLock, "Another tycswap process")
}

func TestRotateCyclesThroughTheRotatableAccounts(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	res, err := sw.Rotate(ctx)
	if err != nil || res.Number != "2" {
		t.Fatalf("Rotate from 1 = %+v %v", res, err)
	}
	res, err = sw.Rotate(ctx)
	if err != nil || res.Number != "1" {
		t.Fatalf("Rotate from 2 = %+v %v", res, err)
	}
}

func TestRotateFromAnUnmanagedLoginGoesToTheFirstCandidate(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	_ = sw.Store().SetDisabled(keyA, true)
	f.writeLive(f.authJSON("stranger", "user-z", "z@x", "rt"))
	res, err := sw.Rotate(ctx)
	if err != nil || res.Number != "2" {
		t.Fatalf("Rotate = %+v %v", res, err)
	}
}

func TestRotateEdgeCases(t *testing.T) {
	f := newFixture(t)
	_, err := f.open().Rotate(ctx)
	wantErr(t, err, cerr.KindSwitch, "No rotatable Codex accounts")

	sw := f.seeded()
	_ = sw.Store().SetDisabled(keyB, true)
	_, err = sw.Rotate(ctx)
	wantErr(t, err, cerr.KindSwitch, "Only one rotatable Codex account")
}

func TestSwitchBestPicksTheMostHeadroom(t *testing.T) {
	f := newFixture(t)
	pcts := map[string]float64{acctA: 50, acctB: 20}
	f.usageFn = func(acct string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": pcts[acct]}}}
	}
	res, err := f.seeded().SwitchBest(ctx)
	if err != nil || res.Number != "2" {
		t.Fatalf("SwitchBest = %+v %v", res, err)
	}
}

func TestSwitchBestWithoutAMeasurementFails(t *testing.T) {
	f := newFixture(t)
	f.usageFn = func(string) api.UsageFetch { return api.UsageFetch{Sentinel: "http 401"} }
	_, err := f.seeded().SwitchBest(ctx)
	wantErr(t, err, cerr.KindSwitch, "No Codex account with a known measurement")
}

// ---- status and token diagnostics --------------------------------------------

func TestStatusWithoutAnActiveAccount(t *testing.T) {
	f := newFixture(t)
	st := f.open().Status(ctx)
	doc := st.JSON()
	if doc["active"] != nil || doc["provider"] != "codex" || doc["schemaVersion"] != 1 {
		t.Errorf("JSON = %v", doc)
	}
	var buf bytes.Buffer
	st.Render(&buf)
	if !strings.Contains(buf.String(), "No active Codex account") {
		t.Errorf("render = %q", buf.String())
	}
}

func TestStatusOfTheActiveAccount(t *testing.T) {
	f := newFixture(t)
	f.usageFn = func(string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 42.0}}}
	}
	sw := f.seeded()
	_ = sw.Store().SetAlias(keyA, "home")
	st := sw.Status(ctx)
	doc := st.JSON()
	active, ok := doc["active"].(map[string]any)
	if !ok {
		t.Fatalf("JSON = %v", doc)
	}
	want := map[string]any{"number": 1, "email": "a@x", "workspace": "", "alias": "home", "plan": "pro", "managed": true, "usageStatus": "ok"}
	for k, v := range want {
		if active[k] != v {
			t.Errorf("active[%q] = %v, want %v", k, active[k], v)
		}
	}
	if active["usage"] == nil {
		t.Error("usage missing")
	}
	if doc["totalManagedAccounts"] != 2 {
		t.Errorf("totalManagedAccounts = %v", doc["totalManagedAccounts"])
	}
	var buf bytes.Buffer
	st.Render(&buf)
	out := buf.String()
	for _, s := range []string{"Codex-1", "a@x", "[personal]", "Total managed Codex accounts: 2", "5h: 42%"} {
		if !strings.Contains(out, s) {
			t.Errorf("render missing %q:\n%s", s, out)
		}
	}
}

func TestTokenStatusNeverReturnsTokenMaterial(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	ts, err := sw.TokenStatus("2")
	if err != nil {
		t.Fatal(err)
	}
	if ts["state"] != "oauth" || ts["hasRefreshToken"] != true || ts["refreshDue"] != false ||
		ts["lastRefresh"] != "2026-08-16T00:00:00Z" {
		t.Errorf("TokenStatus = %v", ts)
	}
	if in, _ := ts["expiresInSeconds"].(float64); in != 3600 {
		t.Errorf("expiresInSeconds = %v, want 3600", ts["expiresInSeconds"])
	}
	raw, _ := json.Marshal(ts)
	snap := tokensOf(t, sw.Store().ReadSnapshot(keyB))
	for _, k := range []string{"access_token", "refresh_token", "id_token"} {
		if v, _ := snap[k].(string); v != "" && strings.Contains(string(raw), v) {
			t.Errorf("token material %s leaked", k)
		}
	}

	_ = sw.Store().DeleteSnapshot(keyB)
	ts, _ = sw.TokenStatus("2")
	if ts["state"] != "no credentials" {
		t.Errorf("TokenStatus without credentials = %v", ts)
	}
	if _, err := sw.TokenStatus("99"); err == nil {
		t.Error("TokenStatus of an unknown account succeeded")
	}
}

// ---- BindingPct ----------------------------------------------------------------

func TestBindingPct(t *testing.T) {
	win := func(p any) map[string]any { return map[string]any{"pct": p} }
	for _, tc := range []struct {
		name string
		in   map[string]any
		want any
	}{
		{"worst window", map[string]any{"five_hour": win(20.0), "seven_day": win(80.0)}, 80.0},
		{"json.Number", map[string]any{"five_hour": win(json.Number("33.5"))}, 33.5},
		{"int", map[string]any{"seven_day": win(12)}, 12.0},
		{"nil usage", nil, nil},
		{"no windows", map[string]any{"spend": 1}, nil},
		{"non-numeric pct", map[string]any{"five_hour": win("high")}, nil},
		{"bool pct", map[string]any{"five_hour": win(true)}, nil},
		{"bad json.Number", map[string]any{"five_hour": win(json.Number("x")), "seven_day": win(3.0)}, 3.0},
		{"window not a map", map[string]any{"five_hour": 50.0}, nil},
	} {
		got := BindingPct(tc.in)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: got %v, want nil", tc.name, *got)
		case tc.want != nil && (got == nil || *got != tc.want.(float64)):
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeAlias(t *testing.T) {
	for in, want := range map[string]string{"Work": "work", " a.b_c-1 ": "a.b_c-1"} {
		if got, err := NormalizeAlias(in); err != nil || got != want {
			t.Errorf("NormalizeAlias(%q) = %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "  ", "123", "-x", "a b", "é"} {
		if _, err := NormalizeAlias(in); cerr.TypeName(err) != string(cerr.KindValidation) {
			t.Errorf("NormalizeAlias(%q) err = %v", in, err)
		}
	}
}

// Disable and remove hold the store lock like a switch does, so they wait for
// a switch or a token refresh that holds it, in this process or another,
// instead of writing between its steps (the store's own writes skip the file
// lock while this process holds it), and a lock that stays held is a lock
// error with nothing written (DESIGN A48).
func TestDisableAndRemoveWaitForTheStoreLock(t *testing.T) {
	f := newFixture(t)
	sw := f.seeded()
	// A second switcher over the same store that waits long enough for the
	// release below; the fixture's own waits 100 ms.
	patient := New(Options{
		Root: f.root, Keychain: f.kc, Platform: platform.Linux, Clock: f.clk,
		Client: f.client(), Stdout: f.out, LockTimeout: 5 * time.Second,
	})
	for _, tc := range []struct {
		name string
		call func() error
		done func() bool
	}{
		{"disable", func() error { _, err := patient.SetAccountDisabled("2", true); return err }, func() bool { return sw.Store().Slots()[1].Disabled }},
		{"remove", func() error { _, err := patient.Remove("2", true); return err }, func() bool { return len(sw.Store().Slots()) == 1 }},
	} {
		held := sw.Store().Lock()
		if ok, err := held.Acquire(time.Second); !ok || err != nil {
			t.Fatalf("%s: cannot take the lock: %v %v", tc.name, ok, err)
		}
		returned := make(chan error, 1)
		go func() { returned <- tc.call() }()
		select {
		case err := <-returned:
			_ = held.Release()
			t.Fatalf("%s returned (%v) while another holder had the store lock", tc.name, err)
		case <-time.After(100 * time.Millisecond):
		}
		if tc.done() {
			t.Errorf("%s wrote while the lock was held", tc.name)
		}
		_ = held.Release()
		select {
		case err := <-returned:
			if err != nil {
				t.Fatalf("%s after the release: %v", tc.name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not finish after the lock was released", tc.name)
		}
		if !tc.done() {
			t.Errorf("%s did not write after the release", tc.name)
		}
	}

	holdLock(t, sw)
	_, err := sw.SetAccountDisabled("1", true)
	wantErr(t, err, cerr.KindLock, "Another tycswap process")
	_, err = sw.Remove("1", true)
	wantErr(t, err, cerr.KindLock, "Another tycswap process")
	if slots := sw.Store().Slots(); len(slots) != 1 || slots[0].Disabled {
		t.Errorf("a busy call wrote: %+v", slots)
	}
}
