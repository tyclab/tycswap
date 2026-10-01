// cache_test.go — Codex usage behind the shared usage.Store: what does and does
// not hit the network. Ports claude-swap PR #252
// tests/test_codex_usage_cache.py, plus the account-id selection, sentinel
// classification and poll-plan wiring its docstrings promise.

package usagecache

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/usage"
)

var (
	cKeyA = authfile.AccountKey("user-a", "acct-a")
	cKeyB = authfile.AccountKey("user-b", "acct-b")
)

// usageDict is USAGE. Float64 percentages so it compares equal after the
// usage.json round trip.
func usageDict() map[string]any {
	return map[string]any{
		"five_hour": map[string]any{"pct": 10.0},
		"seven_day": map[string]any{"pct": 20.0},
	}
}

type cacheFixture struct {
	*env
	cache *Cache
	uc    *usageCounter
	slots []store.Slot
}

// newCacheFixture is the cache + slots + counter fixtures: two pro slots with
// snapshots, and a fake that answers USAGE until told otherwise.
func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	e := newEnv(t)
	st := e.open()
	a := mustUpsert(t, st, cKeyA, store.Upsert{Email: "a@x", Plan: "pro"})
	b := mustUpsert(t, st, cKeyB, store.Upsert{Email: "b@x", Plan: "pro"})
	mustSnapshot(t, st, cKeyA, "acct-a")
	mustSnapshot(t, st, cKeyB, "acct-b")
	uc := &usageCounter{result: api.UsageFetch{Usage: usageDict()}}
	return &cacheFixture{env: e, cache: New(e.open(), uc.client(), e.clk), uc: uc, slots: []store.Slot{a, b}}
}

func (f *cacheFixture) refresh(slots []store.Slot) map[string]usage.UsageEntry {
	return f.cache.Refresh(context.Background(), slots, f.payloadFor, 100, "")
}

func TestColdCacheFetchesEverySlot(t *testing.T) {
	f := newCacheFixture(t)
	entries := f.refresh(f.slots)
	if got := f.uc.count(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
	if !reflect.DeepEqual(entries["1"].LastGood, usageDict()) {
		t.Fatalf("LastGood = %v, want %v", entries["1"].LastGood, usageDict())
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want one per slot", len(entries))
	}
}

// Two `tycswap codex list` calls in a row cost one round of requests, not two.
func TestSecondPassServesFromCacheWithoutARequest(t *testing.T) {
	f := newCacheFixture(t)
	f.refresh(f.slots)
	f.refresh(f.slots)
	if got := f.uc.count(); got != 2 {
		t.Fatalf("calls = %d, want 2 (unchanged by the second pass)", got)
	}
}

func TestCachedValueIsStillServedOnTheSecondPass(t *testing.T) {
	f := newCacheFixture(t)
	f.refresh(f.slots)
	f.clk.Advance(30 * time.Second) // past the claim TTL, inside the serve TTL
	entries := f.refresh(f.slots)
	if f.uc.count() != 2 {
		t.Fatalf("calls = %d, want 2", f.uc.count())
	}
	if !reflect.DeepEqual(entries["1"].LastGood, usageDict()) {
		t.Fatalf("LastGood = %v", entries["1"].LastGood)
	}
	if entries["1"].AgeS == nil || *entries["1"].AgeS != 30 {
		t.Fatalf("AgeS = %v, want 30", entries["1"].AgeS)
	}
}

func TestEntriesNeverTouchesTheNetwork(t *testing.T) {
	f := newCacheFixture(t)
	f.cache.Entries(f.slots)
	if f.uc.count() != 0 {
		t.Fatalf("calls = %d, want 0", f.uc.count())
	}
}

// Two collectors both passing the staleness check and both fetching is exactly
// what the reservation exists to prevent.
func TestSlotReservedByAnotherProcessIsNotFetched(t *testing.T) {
	f := newCacheFixture(t)
	other := New(f.open(), f.uc.client(), f.clk)
	won, err := other.usage.Reserve([]string{"1", "2"}, other.Identities(f.slots), true)
	if err != nil || len(won) != 2 {
		t.Fatalf("other Reserve = %v, %v", won, err)
	}
	f.refresh(f.slots)
	if f.uc.count() != 0 {
		t.Fatalf("calls = %d, want 0", f.uc.count())
	}
}

func TestFailureRecordsAnErrorAndShowsIt(t *testing.T) {
	f := newCacheFixture(t)
	f.uc.set(api.UsageFetch{Sentinel: "http 401"})
	entries := f.refresh(f.slots)
	e := entries["1"]
	if e.Sentinel != "http 401" || e.LastGood != nil {
		t.Fatalf("entry = %+v, want sentinel http 401 and no lastGood", e)
	}
	if e.ConsecutiveFailures != 1 || e.LastError != "http 401" {
		t.Fatalf("failures=%d lastError=%q, want 1/http 401", e.ConsecutiveFailures, e.LastError)
	}
}

// Sentinels are derived and never persisted: the cached read model carries
// the recorded error, not the overlay.
func TestSentinelOverlayIsNotPersisted(t *testing.T) {
	f := newCacheFixture(t)
	f.uc.set(api.UsageFetch{Sentinel: "http 401"})
	f.refresh(f.slots)
	e := f.cache.Entries(f.slots)["1"]
	if e.Sentinel != "" {
		t.Fatalf("persisted sentinel %q", e.Sentinel)
	}
	if e.LastError != "http 401" {
		t.Fatalf("LastError = %q", e.LastError)
	}
}

// Serving stale-with-age beats serving blank.
func TestFailureDoesNotDestroyThePreviousGoodValue(t *testing.T) {
	f := newCacheFixture(t)
	f.refresh(f.slots)
	f.uc.set(api.UsageFetch{Sentinel: "http 500"})
	f.clk.Advance(24 * time.Hour)
	entries := f.refresh(f.slots)
	if f.uc.count() != 4 {
		t.Fatalf("calls = %d, want 4", f.uc.count())
	}
	e := entries["1"]
	if !reflect.DeepEqual(e.LastGood, usageDict()) {
		t.Fatalf("LastGood = %v, want preserved", e.LastGood)
	}
	if e.ConsecutiveFailures < 1 || e.Sentinel != "http 500" {
		t.Fatalf("entry = %+v", e)
	}
}

// After an error the slot is in backoff: a later pass, stale and past the
// claim TTL but inside the backoff window, makes no request.
func TestBackoffAfterAnErrorSuppressesTheNextRequest(t *testing.T) {
	f := newCacheFixture(t)
	f.uc.set(api.UsageFetch{Sentinel: "http 500"})
	f.refresh(f.slots)
	f.clk.Advance(20 * time.Second) // < 30 s first backoff, > 10 s claim TTL
	f.refresh(f.slots)
	if f.uc.count() != 2 {
		t.Fatalf("calls = %d, want 2 (second pass in backoff)", f.uc.count())
	}
	f.clk.Advance(20 * time.Second) // backoff lifted
	f.refresh(f.slots)
	if f.uc.count() != 4 {
		t.Fatalf("calls = %d, want 4 once backoff lifts", f.uc.count())
	}
}

func TestRetryAfterIsCarriedIntoTheRecord(t *testing.T) {
	f := newCacheFixture(t)
	ra := 120.0
	f.uc.set(api.UsageFetch{Sentinel: "http 429", RetryAfterS: &ra})
	now := clock.Seconds(f.clk)
	entries := f.refresh(f.slots)
	bu := entries["1"].BackoffUntil
	if bu == nil || *bu != now+120 {
		t.Fatalf("BackoffUntil = %v, want now+120 (Retry-After over the 30 s curve)", bu)
	}
	f.clk.Advance(100 * time.Second)
	f.refresh(f.slots)
	if f.uc.count() != 2 {
		t.Fatalf("calls = %d, want 2 (Retry-After honoured)", f.uc.count())
	}
}

func TestSuccessWritesAPollPlan(t *testing.T) {
	f := newCacheFixture(t)
	entries := f.refresh(f.slots)
	if entries["1"].NextPollAt == nil || entries["1"].PollIntervalS == nil {
		t.Fatalf("entry = %+v, want a poll plan", entries["1"])
	}
}

// is_active reaches the planner: with no history the active slot starts at
// MIN_INTERVAL_S, a candidate at CANDIDATE_DEFAULT_INTERVAL_S.
func TestActiveSlotGetsTheActiveCadence(t *testing.T) {
	f := newCacheFixture(t)
	entries := f.cache.Refresh(context.Background(), f.slots, f.payloadFor, 100, "1")
	if got := *entries["1"].PollIntervalS; got != usage.MinIntervalS {
		t.Fatalf("active interval = %v, want %v", got, usage.MinIntervalS)
	}
	if got := *entries["2"].PollIntervalS; got != usage.CandidateDefaultIntervalS {
		t.Fatalf("candidate interval = %v, want %v", got, usage.CandidateDefaultIntervalS)
	}
}

func TestFailureWritesNoPollPlan(t *testing.T) {
	f := newCacheFixture(t)
	f.uc.set(api.UsageFetch{Sentinel: "http 500"})
	entries := f.refresh(f.slots)
	if entries["1"].NextPollAt != nil {
		t.Fatalf("NextPollAt = %v, want none on failure", *entries["1"].NextPollAt)
	}
}

func TestAPIKeySlotIsASentinelAndMakesNoRequest(t *testing.T) {
	e := newEnv(t)
	uc := &usageCounter{result: api.UsageFetch{Usage: usageDict()}}
	st := e.open()
	sl := mustUpsert(t, st, cKeyA, store.Upsert{Email: "a@x", AuthMode: "apikey"})
	mustSnapshot(t, st, cKeyA, "acct-a")
	entries := New(st, uc.client(), e.clk).Refresh(context.Background(), []store.Slot{sl}, e.payloadFor, 100, "")
	if uc.count() != 0 {
		t.Fatalf("calls = %d, want 0", uc.count())
	}
	if entries["1"].Sentinel != SentinelAPIKey {
		t.Fatalf("Sentinel = %q, want %q", entries["1"].Sentinel, SentinelAPIKey)
	}
}

// A structurally unusable account must not accrue backoff it can never clear
// by itself: "no credentials", "api key" and the server-side MissingAuth are
// no-ops, anything else is a failure.
func TestSentinelClassification(t *testing.T) {
	cases := []struct {
		name         string
		authMode     string
		noSnapshot   bool
		fetch        api.UsageFetch
		wantSentinel string
		wantFailures int
		wantCalls    int
	}{
		{name: "missing snapshot", noSnapshot: true, wantSentinel: SentinelNoCredentials, wantFailures: 0, wantCalls: 0},
		{name: "api key", authMode: "apikey", wantSentinel: SentinelAPIKey, wantFailures: 0, wantCalls: 0},
		{name: "server MissingAuth", fetch: api.UsageFetch{Sentinel: api.SentinelMissingAuth}, wantSentinel: api.SentinelMissingAuth, wantFailures: 0, wantCalls: 1},
		{name: "network", fetch: api.UsageFetch{Sentinel: api.SentinelNetwork}, wantSentinel: api.SentinelNetwork, wantFailures: 1, wantCalls: 1},
		{name: "no usage no sentinel is bad-response", fetch: api.UsageFetch{}, wantSentinel: api.SentinelBadResponse, wantFailures: 1, wantCalls: 1},
		{name: "http 403", fetch: api.UsageFetch{Sentinel: "http 403"}, wantSentinel: "http 403", wantFailures: 1, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			uc := &usageCounter{result: tc.fetch}
			st := e.open()
			sl := mustUpsert(t, st, cKeyA, store.Upsert{Email: "a@x", Plan: "pro", AuthMode: tc.authMode})
			if !tc.noSnapshot {
				mustSnapshot(t, st, cKeyA, "acct-a")
			}
			c := New(st, uc.client(), e.clk)
			got := c.Refresh(context.Background(), []store.Slot{sl}, e.payloadFor, 100, "")["1"]
			if got.Sentinel != tc.wantSentinel {
				t.Errorf("Sentinel = %q, want %q", got.Sentinel, tc.wantSentinel)
			}
			if got.ConsecutiveFailures != tc.wantFailures {
				t.Errorf("ConsecutiveFailures = %d, want %d", got.ConsecutiveFailures, tc.wantFailures)
			}
			if tc.wantFailures == 0 && got.BackoffUntil != nil {
				t.Errorf("BackoffUntil = %v, want none", *got.BackoffUntil)
			}
			if uc.count() != tc.wantCalls {
				t.Errorf("calls = %d, want %d", uc.count(), tc.wantCalls)
			}
		})
	}
}

// The account id sent is tokens.account_id, else the key's account id; the
// access token is tokens.access_token.
func TestAccountIDSentToTheUsageEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		payload  map[string]any
		wantTok  string
		wantAcct string
	}{
		{name: "tokens account id", payload: map[string]any{"tokens": map[string]any{"access_token": "at", "account_id": "from-tokens"}}, wantTok: "at", wantAcct: "from-tokens"},
		{name: "key fallback", payload: map[string]any{"tokens": map[string]any{"access_token": "at"}}, wantTok: "at", wantAcct: "acct-a"},
		{name: "no tokens block", payload: map[string]any{}, wantTok: "", wantAcct: "acct-a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			uc := &usageCounter{result: api.UsageFetch{Usage: usageDict()}}
			st := e.open()
			sl := mustUpsert(t, st, cKeyA, store.Upsert{Email: "a@x", Plan: "pro"})
			New(st, uc.client(), e.clk).Refresh(context.Background(), []store.Slot{sl},
				func(store.Slot) map[string]any { return tc.payload }, 100, "")
			if len(uc.calls) != 1 || uc.calls[0] != (call{tc.wantTok, tc.wantAcct}) {
				t.Fatalf("calls = %v, want [{%s %s}]", uc.calls, tc.wantTok, tc.wantAcct)
			}
		})
	}
}

// A slot re-created for a different account must not inherit the previous
// account's percentages.
func TestIdentityGuardHidesAReusedSlotsOldUsage(t *testing.T) {
	f := newCacheFixture(t)
	f.refresh(f.slots)
	st := f.open()
	if _, err := st.RemoveSlot(cKeyA); err != nil {
		t.Fatal(err)
	}
	fresh := mustUpsert(t, st, authfile.AccountKey("user-c", "acct-c"), store.Upsert{Email: "c@x", Plan: "pro"})
	if fresh.Number != "1" {
		t.Fatalf("fresh.Number = %q, want 1 (same slot, different account)", fresh.Number)
	}
	if lg := f.cache.Entries([]store.Slot{fresh})["1"].LastGood; lg != nil {
		t.Fatalf("LastGood = %v, want none", lg)
	}
}

// One user with two workspaces has one email and two accounts; the email alone
// would let one workspace's usage serve the other.
func TestIdentityUsesTheAccountIDNotJustTheEmail(t *testing.T) {
	sl := store.Slot{Number: "1", AccountKey: authfile.AccountKey("user-a", "acct-x"), Email: "a@x"}
	if got, want := IdentityFor(sl), (usage.Identity{Email: "a@x", OrgUUID: "acct-x"}); got != want {
		t.Fatalf("IdentityFor = %+v, want %+v", got, want)
	}
}

func TestIdentitiesKeyedBySlotNumber(t *testing.T) {
	f := newCacheFixture(t)
	got := f.cache.Identities(f.slots)
	want := map[string]usage.Identity{"1": {Email: "a@x", OrgUUID: "acct-a"}, "2": {Email: "b@x", OrgUUID: "acct-b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Identities = %v, want %v", got, want)
	}
}

func TestRefreshOfNoSlotsIsANoop(t *testing.T) {
	f := newCacheFixture(t)
	got := f.refresh(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("Refresh(nil) = %v, want empty map", got)
	}
	if f.uc.count() != 0 {
		t.Fatalf("calls = %d, want 0", f.uc.count())
	}
}
