// cache.go — Codex usage fetching behind the same cache the Claude side uses.
// Implements claude-swap PR #252 codex/usage_cache.py.
//
// A live fetch on every call costs one request per account per `tycswap codex
// list`: fine for a human at a terminal, unacceptable for an auto loop that
// ticks every few minutes. This package puts Codex behind usage.Store and the
// poll policy, which already implement — and have tests for — the serve TTL,
// cross-process fetch reservation, failure backoff, 429 handling with
// Retry-After, and an adaptive cadence that speeds up when usage is moving.
//
// None of that is reimplemented here. usage.Store keeps an opaque lastGood map
// guarded by an identity pair, so it was already provider-neutral; Cache is the
// adapter that decides which slots need a request, performs those, and records
// the outcomes.
//
// Identity for Codex is (email, chatgpt_account_id). The store's guard exists
// so a slot reused for a different account never serves its predecessor's
// usage; the account id is exactly what distinguishes two workspaces of one
// user, so it takes the OrgUUID position the Claude side uses.
//
// The active-account rule lives in the caller. PayloadFor is supplied by the
// Codex switcher, which is what enforces "never refresh the active account from
// its snapshot". Keeping it a callback means this package never needs to know
// that rule, and the rule stays in one place.

// Package usagecache is the Codex adapter over the shared usage.Store: which
// slots to fetch, how to record them, and the workspace-name refresh.
package usagecache

import (
	"context"
	"strings"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/usage"
)

// Sentinels this package derives itself, before any request is made.
const (
	SentinelAPIKey        = "api key"
	SentinelNoCredentials = "no credentials"
)

// nonFailureSentinels are recorded as no-ops rather than failures. Sentinels
// are derived state, re-computed every pass and never persisted, so a
// structurally unusable account does not accrue backoff it can never clear.
var nonFailureSentinels = map[string]bool{
	api.SentinelMissingAuth: true,
	SentinelAPIKey:          true,
	SentinelNoCredentials:   true,
}

// PayloadFor returns the auth.json payload to fetch a slot's usage with, or
// nil when the slot has no usable credentials. The caller owns the
// never-refresh-the-active-account rule.
type PayloadFor func(s store.Slot) map[string]any

// Cache decides which Codex slots need a request, fetches those, and records
// them in the shared usage table under the store's cache directory.
type Cache struct {
	st     *store.Store
	client api.Client
	clk    clock.Clock
	usage  *usage.Store
}

// New returns a Cache over st's cache directory. A nil clk is the system clock.
func New(st *store.Store, client api.Client, clk clock.Clock) *Cache {
	if clk == nil {
		clk = clock.System{}
	}
	return &Cache{st: st, client: client, clk: clk, usage: usage.NewStore(st.CacheDir(), clk)}
}

// accountID is the chatgpt_account_id half of an account key (the part after
// the last "::", Python's rpartition).
func accountID(key string) string {
	if i := strings.LastIndex(key, "::"); i >= 0 {
		return key[i+2:]
	}
	return key
}

// userID is the chatgpt_user_id half of an account key (Python's partition:
// everything before the first "::", or the whole key when there is none).
func userID(key string) string {
	if i := strings.Index(key, "::"); i >= 0 {
		return key[:i]
	}
	return key
}

// IdentityFor is the store's identity guard for one slot.
func IdentityFor(s store.Slot) usage.Identity {
	return usage.Identity{Email: s.Email, OrgUUID: accountID(s.AccountKey)}
}

// Identities maps each slot number to its identity guard.
func (c *Cache) Identities(slots []store.Slot) map[string]usage.Identity {
	out := make(map[string]usage.Identity, len(slots))
	for _, s := range slots {
		out[s.Number] = IdentityFor(s)
	}
	return out
}

// Entries is the cached read model for these slots. It never touches the
// network.
func (c *Cache) Entries(slots []store.Slot) map[string]usage.UsageEntry {
	return c.usage.Entries(c.Identities(slots))
}

// Refresh fetches whichever of slots is genuinely due, then returns entries
// for them all. A slot that is fresh, in backoff, or already reserved by
// another process is served from cache without a request — that is the entire
// point.
//
// threshold feeds the poll planner's escalation band (Python's default is
// 100); activeNumber marks the live slot for its faster cadence.
func (c *Cache) Refresh(ctx context.Context, slots []store.Slot, payloadFor PayloadFor, threshold float64, activeNumber string) map[string]usage.UsageEntry {
	if len(slots) == 0 {
		return map[string]usage.UsageEntry{}
	}

	byNumber := make(map[string]store.Slot, len(slots))
	numbers := make([]string, 0, len(slots))
	for _, s := range slots {
		if _, dup := byNumber[s.Number]; !dup {
			numbers = append(numbers, s.Number)
		}
		byNumber[s.Number] = s
	}
	identities := c.Identities(slots)

	// respectPlans=true: the on-demand caller contract. Fetch only when the
	// entry is both stale and poll-due, so a second `tycswap codex list` seconds
	// after the first costs nothing. A reserve error claims nothing, which
	// degrades to serving the cache.
	claims, _ := c.usage.Reserve(numbers, identities, true)

	sentinels := map[string]string{}
	if len(claims) > 0 {
		before := c.usage.Entries(identities)
		outcomes := make(map[string]usage.FetchRecord, len(claims))
		plans := map[string]usage.PollPlan{}

		for _, num := range claims {
			slot := byNumber[num]
			var payload map[string]any
			if slot.AuthMode != "apikey" && payloadFor != nil {
				payload = payloadFor(slot)
			}
			rec, plan := c.fetchOne(ctx, slot, payload, before[num], threshold, num == activeNumber)
			outcomes[num] = rec
			if plan != nil {
				plans[num] = *plan
			}
		}

		// Record then replan, as the Claude collector does; this store version
		// has no single-transaction plan argument. Write errors are non-fatal:
		// the next pass simply fetches again.
		_ = c.usage.Record(outcomes, identities)
		_ = c.usage.SetPollPlan(plans, identities)

		// A sentinel is a live overlay, re-derived every pass and never
		// persisted (see usage.UsageEntry). The store therefore cannot hand it
		// back, so this pass's sentinels are laid over the read here —
		// otherwise "api key" or a 401 would render as a blank row.
		for num, rec := range outcomes {
			if rec.Sentinel != "" {
				sentinels[num] = rec.Sentinel
			} else if rec.Error != "" {
				sentinels[num] = rec.Error
			}
		}
	}

	entries := c.usage.Entries(identities)
	for num, sentinel := range sentinels {
		entries[num] = usage.WithSentinel(entries[num], sentinel)
	}
	return entries
}

// fetchOne performs one account's fetch and returns it as a record the store
// can merge, plus the poll plan for a success (nil otherwise).
func (c *Cache) fetchOne(ctx context.Context, slot store.Slot, payload map[string]any, prev usage.UsageEntry, threshold float64, isActive bool) (usage.FetchRecord, *usage.PollPlan) {
	if slot.AuthMode == "apikey" {
		return usage.FetchRecord{Sentinel: SentinelAPIKey}, nil
	}
	if payload == nil {
		return usage.FetchRecord{Sentinel: SentinelNoCredentials}, nil
	}

	tokens, _ := payload["tokens"].(map[string]any)
	token, _ := tokens["access_token"].(string)
	acct, _ := tokens["account_id"].(string)
	if acct == "" {
		acct = accountID(slot.AccountKey)
	}
	result := c.client.FetchUsage(ctx, token, acct)

	if result.Usage != nil {
		now := clock.Seconds(c.clk)
		recent429 := prev.Last429At != nil && (now-*prev.Last429At) < usage.Recent429WindowS
		next, interval := usage.PlanAfterFetch(usage.PlanInput{
			PrevIntervalS: prev.PollIntervalS,
			PrevUsage:     prev.LastGood,
			NewUsage:      result.Usage,
			IsActive:      isActive,
			Threshold:     threshold,
			Models:        nil,
			Recent429:     recent429,
			Now:           now,
		})
		return usage.FetchRecord{Usage: result.Usage}, &usage.PollPlan{NextPollAt: &next, IntervalS: &interval}
	}

	sentinel := result.Sentinel
	if sentinel == "" {
		sentinel = api.SentinelBadResponse
	}
	if nonFailureSentinels[sentinel] {
		// Derived state, not a server failure: recording it as an error would
		// accrue backoff the account can never clear by itself.
		return usage.FetchRecord{Sentinel: sentinel}, nil
	}
	return usage.FetchRecord{Error: sentinel, RetryAfterS: result.RetryAfterS}, nil
}
