// cache.go — Codex usage behind the Claude side's usage.Store and poll policy (claude-swap PR #252 codex/usage_cache.py), so an auto
// loop does not cost a request per account per tick: serve TTL, fetch reservation, backoff and Retry-After are not reimplemented.
// Codex identity is (email, chatgpt_account_id): the account id takes the OrgUUID position, telling a user's workspaces apart.
// The never-refresh-the-active-account rule stays in the Codex switcher, which supplies PayloadFor.

// Package usagecache is the Codex adapter over the shared usage.Store: which slots to fetch, how to record them, workspace names.
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

func accountID(key string) string {
	if i := strings.LastIndex(key, "::"); i >= 0 {
		return key[i+2:]
	}
	return key
}

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

func (c *Cache) Refresh(ctx context.Context, slots []store.Slot, payloadFor PayloadFor, threshold float64, activeNumber string) map[string]usage.UsageEntry {
	return c.refresh(ctx, slots, payloadFor, threshold, activeNumber, true)
}

func (c *Cache) RefreshCurrent(ctx context.Context, slot store.Slot, payloadFor PayloadFor, threshold float64) error {
	slots := []store.Slot{slot}
	now := clock.Seconds(c.clk)
	if err := c.usage.SetPollPlan(map[string]usage.PollPlan{slot.Number: {NextPollAt: &now}}, c.Identities(slots)); err != nil {
		return err
	}
	c.refresh(ctx, slots, payloadFor, threshold, slot.Number, false)
	return nil
}

func (c *Cache) refresh(ctx context.Context, slots []store.Slot, payloadFor PayloadFor, threshold float64, activeNumber string, respectPlans bool) map[string]usage.UsageEntry {
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

	// Fetch only when stale and poll-due, so a second `tycswap codex list` seconds later costs nothing.
	claims, _ := c.usage.Reserve(numbers, identities, respectPlans)

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

		_ = c.usage.Record(outcomes, identities)
		_ = c.usage.SetPollPlan(plans, identities)

		// Sentinels are never persisted, so the store cannot hand them back; without this overlay a 401 renders as a blank row.
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
