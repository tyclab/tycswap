// switcher.go — the Codex provider's verbs: add, switch, rotate, remove, alias,
// disable, status, token diagnostics, renumbering and the one-pass read model.
// Port of claude-swap PR #252 codex/switcher.py (CodexSwitcher).
//
// Small on purpose: the Claude switcher's provenance machinery has no analogue
// here and is not imitated. Two rules carry this package, and both exist
// because the codex CLI owns ~/.codex/auth.json and writes to it whenever it
// likes.
//
//  1. The live file decides who is active. CurrentAccountNumber reads the live
//     file's identity and matches it to a slot; the store's activeAccountKey
//     records intent only. A session left open on account A can rewrite the
//     live file minutes after a switch to B, and a registry-derived answer
//     would report B while every codex command runs as A.
//  2. The active account is never refreshed from its snapshot. The codex CLI
//     holds the same refresh token and keeps the live file current; if the
//     server rotates refresh tokens, two parties refreshing the same one means
//     one of them is logged out. So the active account reads the live payload,
//     inactive accounts refresh from their snapshots, and a rotated token is
//     written back immediately, before it is used for anything else.
//
// Both of those writes happen under the Codex store's own file lock: a TUI
// refresh and a CLI switch in another terminal are concurrent by construction,
// and an interleaved capture/write would put one account's tokens in another's
// slot. The lock is the Codex store's, so it never contends with a Claude
// switch. Usage goes through usagecache, the adapter onto the same usage table
// and poll policy the Claude side uses, so two consecutive listings cost one
// round of requests, not two.
package switcher

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/procdetect"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/codex/transfer"
	"github.com/tyclab/tycswap/internal/codex/usagecache"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/usage"
)

// ProviderID is this provider's id, matching AccountSnapshot.Provider.
const ProviderID = reporting.ProviderCodex

// DefaultLockTimeout is how long a verb waits for the store lock (switcher.py
// _lock's 10s). A usage refresh waits 1.5x that (switcher.py's 15s).
const DefaultLockTimeout = 10 * time.Second

// usageThreshold feeds the poll planner's escalation band; switcher.py calls
// the cache without one, so the cache's documented default of 100 applies.
const usageThreshold = 100.0

const busyMsg = "Another tycswap process is using the Codex store; try again."

// Options configures a Switcher. Zero fields take their production defaults.
type Options struct {
	// Root is the Codex store root; "" is authfile.StoreRoot().
	Root string
	// Keychain and Platform are passed to store.Options unchanged.
	Keychain keychain.KeychainClient
	Platform platform.Platform
	// Clock defaults to clock.System{}.
	Clock clock.Clock
	// Client defaults to api.NewHTTPClient().
	Client api.Client
	// Stdout receives warnings and prompts; defaults to os.Stdout.
	Stdout io.Writer
	// Stdin answers Remove's confirmation prompt; defaults to os.Stdin.
	Stdin io.Reader
	// RunningPIDs detects codex sessions; defaults to procdetect.RunningCodexPIDs.
	RunningPIDs func() []int
	// LockTimeout defaults to DefaultLockTimeout.
	LockTimeout time.Duration
}

// Switcher is the multi-account switcher for the Codex CLI.
type Switcher struct {
	st          *store.Store
	cache       *usagecache.Cache
	client      api.Client
	clk         clock.Clock
	out         io.Writer
	in          io.Reader
	runningPIDs func() []int
	lockTimeout time.Duration

	// Seams the tests replace (switcher.py's monkeypatched module functions).
	writeLive    func(map[string]any) (string, error)
	needsRefresh func(payload any, now float64) bool
}

// SwitchResult is what a switch did, including anything the user must act on.
// A non-empty RunningPIDs means codex sessions must be restarted before the
// new account takes effect; they are reported, never blocked on.
type SwitchResult struct {
	Number      string
	Email       string
	RunningPIDs []int
	// AlreadyActive reports that the target was already the live login: its
	// tokens were captured and auth.json was left untouched.
	AlreadyActive bool
}

// New returns a Switcher with Options defaults filled in.
func New(o Options) *Switcher {
	clk := o.Clock
	if clk == nil {
		clk = clock.System{}
	}
	client := o.Client
	if client == nil {
		client = api.NewHTTPClient()
	}
	st := store.New(store.Options{Root: o.Root, Keychain: o.Keychain, Clock: clk, Platform: o.Platform})
	s := &Switcher{
		st:           st,
		cache:        usagecache.New(st, client, clk),
		client:       client,
		clk:          clk,
		out:          o.Stdout,
		in:           o.Stdin,
		runningPIDs:  o.RunningPIDs,
		lockTimeout:  o.LockTimeout,
		writeLive:    authfile.WriteLiveAuth,
		needsRefresh: api.NeedsRefresh,
	}
	if s.out == nil {
		s.out = os.Stdout
	}
	if s.in == nil {
		s.in = os.Stdin
	}
	if s.runningPIDs == nil {
		s.runningPIDs = procdetect.RunningCodexPIDs
	}
	if s.lockTimeout <= 0 {
		s.lockTimeout = DefaultLockTimeout
	}
	return s
}

// Store returns the underlying Codex store.
func (s *Switcher) Store() *store.Store { return s.st }

// Cache returns the usage cache.
func (s *Switcher) Cache() *usagecache.Cache { return s.cache }

// acquire takes the store lock or returns the busy error. The returned release
// func must be called exactly once.
func (s *Switcher) acquire(timeout time.Duration) (func(), error) {
	l := s.st.Lock()
	ok, err := l.Acquire(timeout)
	if err != nil {
		return nil, cerr.Switch(busyMsg).Wrap(err)
	}
	if !ok {
		return nil, cerr.Switch(busyMsg)
	}
	return func() { _ = l.Release() }, nil
}

func (s *Switcher) withLock(fn func() error) error {
	release, err := s.acquire(s.lockTimeout)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// ---- identity --------------------------------------------------------------

// CurrentAccountNumber is the slot the live file currently holds, or "" when
// the live login is absent, unidentifiable or unmanaged.
func (s *Switcher) CurrentAccountNumber() string {
	payload := authfile.ReadLivePayload()
	if payload == nil {
		return ""
	}
	id := authfile.ParseIdentity(payload)
	if id == nil || !id.Identifiable() {
		return ""
	}
	if slot := s.st.SlotForKey(id.AccountKey()); slot != nil {
		return slot.Number
	}
	return ""
}

// ResolveAccount resolves a slot number, email or alias to (number, email,
// display label).
func (s *Switcher) ResolveAccount(identifier string) (number, email, label string, err error) {
	slot, err := transfer.ResolveSlot(s.st, identifier)
	if err != nil {
		return "", "", "", err
	}
	return slot.Number, slot.Email, slot.DisplayLabel(), nil
}

// ---- credentials -----------------------------------------------------------

// captureLive stores the live login into the slot its own identity matches.
// Matching on the live file's identity rather than on the registry's idea of
// the active slot is what makes this repair a clobber instead of committing
// one. An unmanaged login is not ours to store.
func (s *Switcher) captureLive() error {
	payload := authfile.ReadLivePayload()
	if payload == nil {
		return nil
	}
	id := authfile.ParseIdentity(payload)
	if id == nil || !id.Identifiable() {
		return nil
	}
	slot := s.st.SlotForKey(id.AccountKey())
	if slot == nil {
		return nil
	}
	return s.st.WriteSnapshot(slot.AccountKey, payload)
}

// payloadForUsage is the payload to read slot's usage from, refreshed if
// needed. The active slot is read from the live file and never refreshed.
func (s *Switcher) payloadForUsage(ctx context.Context, slot store.Slot, active string) map[string]any {
	if slot.Number == active {
		return authfile.ReadLivePayload()
	}
	payload := s.st.ReadSnapshot(slot.AccountKey)
	if payload == nil || slot.AuthMode == "apikey" {
		return payload
	}
	if !s.needsRefresh(payload, clock.Seconds(s.clk)) {
		return payload
	}
	// The lock is taken BEFORE the refresh, not around the persist alone. If
	// the server rotates the refresh token the old one may die the instant the
	// response is issued, so a refresh we cannot persist is a refresh we must
	// not perform: refusing costs a stale row, refreshing without persisting
	// can cost the account. A busy store skips this account's refresh.
	release, err := s.acquire(s.lockTimeout * 3 / 2)
	if err != nil {
		return payload
	}
	defer release()
	// Everything above was read without the lock, so it is only a hint. Another
	// process may have refreshed this slot (and rotated the token we hold) or
	// made it the active account while we waited; decide again from what is on
	// disk now. The active account is never refreshed from its snapshot.
	if s.CurrentAccountNumber() == slot.Number {
		return authfile.ReadLivePayload()
	}
	payload = s.st.ReadSnapshot(slot.AccountKey)
	if payload == nil || !s.needsRefresh(payload, clock.Seconds(s.clk)) {
		return payload
	}
	outcome := s.client.TryRefresh(ctx, payload)
	if outcome.Payload == nil {
		return payload
	}
	if err := s.st.WriteSnapshot(slot.AccountKey, outcome.Payload); err != nil {
		// Not persisted: keep serving what is on disk rather than a token the
		// store does not hold.
		return payload
	}
	return outcome.Payload
}

// ---- read model ------------------------------------------------------------

// usageFor returns usage for every slot, refreshing those eligible and due.
// fetch has the Claude side's documented semantics, which the TUI relies on:
// nil makes every account eligible, and a set restricts which accounts may be
// fetched this pass (an empty set means no network at all).
func (s *Switcher) usageFor(ctx context.Context, slots []store.Slot, fetch map[string]bool, active string) map[string]usage.UsageEntry {
	if len(slots) == 0 {
		return map[string]usage.UsageEntry{}
	}
	var wanted []store.Slot
	for _, sl := range slots {
		if fetch == nil || fetch[sl.Number] {
			wanted = append(wanted, sl)
		}
	}
	if len(wanted) == 0 {
		return s.cache.Entries(slots)
	}
	payloadFor := func(sl store.Slot) map[string]any { return s.payloadForUsage(ctx, sl, active) }
	refreshed := s.cache.Refresh(ctx, wanted, payloadFor, usageThreshold, active)

	// Workspace names ride along with a usage fetch: they change approximately
	// never, and a missing name is cosmetic, so it must never fail a listing.
	func() {
		defer func() { _ = recover() }()
		usagecache.RefreshWorkspaceNames(ctx, s.st, s.client, payloadFor)
	}()

	entries := s.cache.Entries(slots)
	for num, e := range refreshed {
		entries[num] = e
	}
	return entries
}

// AccountsSnapshot is one coherent pass over every managed Codex account.
func (s *Switcher) AccountsSnapshot(ctx context.Context, fetch map[string]bool) reporting.AccountsSnapshot {
	active := s.CurrentAccountNumber()
	entries := s.usageFor(ctx, s.st.Slots(), fetch, active)
	// Re-read: a fetching pass may have filled in workspace names.
	slots := s.st.Slots()
	rows := make([]reporting.AccountSnapshot, 0, len(slots))
	for _, sl := range slots {
		entry := entries[sl.Number]
		kind := "oauth"
		if sl.AuthMode == "apikey" {
			// Derived every pass, never persisted: an API-key account has no
			// usage to fetch, and a stored sentinel would outlive the fact.
			entry = usage.UsageEntry{Sentinel: usagecache.SentinelAPIKey}
			kind = "api_key"
		}
		switchable := sl.AuthMode != "apikey"
		rows = append(rows, reporting.AccountSnapshot{
			Number:           sl.Number,
			Email:            sl.Email,
			OrgName:          sl.WorkspaceName,
			IsActive:         sl.Number == active,
			Kind:             kind,
			Switchable:       switchable,
			Usage:            entry,
			Alias:            sl.Alias,
			Disabled:         sl.Disabled,
			RotationEligible: switchable && !sl.Disabled,
			Provider:         ProviderID,
		})
	}
	return reporting.AccountsSnapshot{
		ActiveNumber: active,
		Accounts:     rows,
		TakenAt:      clock.Seconds(s.clk),
		Provider:     ProviderID,
	}
}

// ---- verbs -----------------------------------------------------------------

// Add captures whoever is currently logged in to the codex CLI, optionally
// under alias, and makes it the active slot.
func (s *Switcher) Add(ctx context.Context, alias string) (store.Slot, error) {
	payload := authfile.ReadLivePayload()
	var id *authfile.Identity
	if payload != nil {
		id = authfile.ParseIdentity(payload)
	}
	if payload == nil || id == nil {
		return store.Slot{}, cerr.Switch("No Codex login found. Run 'tycswap codex login' (or 'codex login') first.")
	}
	if !id.Identifiable() {
		return store.Slot{}, cerr.Switch("The current Codex login carries no account id, so it cannot be " +
			"told apart from another account. API-key logins are not switchable.")
	}
	normalized := ""
	if alias != "" {
		n, err := NormalizeAlias(alias)
		if err != nil {
			return store.Slot{}, err
		}
		normalized = n
	}
	authMode := "chatgpt"
	if id.IsAPIKey {
		authMode = "apikey"
	}
	key := id.AccountKey()
	var slot store.Slot
	err := s.withLock(func() error {
		var err error
		if slot, err = s.st.UpsertSlot(key, store.Upsert{Email: id.Email, Plan: id.Plan, AuthMode: authMode}); err != nil {
			return err
		}
		if err := s.st.WriteSnapshot(key, payload); err != nil {
			return err
		}
		if err := s.st.SetActive(key); err != nil {
			return err
		}
		if normalized != "" {
			if err := s.st.SetAlias(key, normalized); err != nil {
				return err
			}
			slot.Alias = normalized
		}
		return nil
	})
	if err != nil {
		return store.Slot{}, err
	}
	return slot, nil
}

// SwitchTo activates a stored account. It is held under the store lock end to
// end: capture-then-write is exactly the sequence a concurrent switch or TUI
// refresh could interleave with. A failed live write is rolled back.
func (s *Switcher) SwitchTo(ctx context.Context, identifier string) (SwitchResult, error) {
	slot, err := transfer.ResolveSlot(s.st, identifier)
	if err != nil {
		return SwitchResult{}, err
	}
	already := false
	err = s.withLock(func() error {
		if err := s.captureLive(); err != nil {
			return cerr.Switch("Failed to capture the live Codex login: %v", err).Wrap(err)
		}
		// Already live: the capture above just stored the freshest tokens, and
		// writing the snapshot back would only replace the live file with an
		// older copy of itself — or with a refresh token codex has since
		// rotated away. Record the intent and leave auth.json alone.
		if s.CurrentAccountNumber() == slot.Number {
			already = true
			return s.st.SetActive(slot.AccountKey)
		}
		// Read AFTER the capture, so the target is what the store now holds.
		target := s.st.ReadSnapshot(slot.AccountKey)
		if target == nil {
			return cerr.Switch("Codex account %s has no stored credentials", slot.Number)
		}
		previousLive := authfile.ReadLivePayload()
		previousActive := s.st.ActiveKey()

		if _, err := s.writeLive(target); err != nil {
			// Put the live file back exactly as it was; a half-switched login
			// is worse than a failed one.
			msg := fmt.Sprintf("Failed to activate Codex account: %v", err)
			if previousLive != nil {
				if _, rbErr := s.writeLive(previousLive); rbErr != nil {
					msg += fmt.Sprintf(" (rollback also failed: %v)", rbErr)
				}
			}
			if saErr := s.st.SetActive(previousActive); saErr != nil {
				msg += fmt.Sprintf(" (restoring the active account also failed: %v)", saErr)
			}
			return cerr.Switch("%s", msg).Wrap(err)
		}
		return s.st.SetActive(slot.AccountKey)
	})
	if err != nil {
		return SwitchResult{}, err
	}
	if already {
		// Nothing was switched, so no session needs a restart.
		return SwitchResult{Number: slot.Number, Email: slot.Email, RunningPIDs: []int{}, AlreadyActive: true}, nil
	}
	// Outside the lock: process detection is advisory and shells out.
	pids := s.runningPIDs()
	if pids == nil {
		pids = []int{}
	}
	return SwitchResult{Number: slot.Number, Email: slot.Email, RunningPIDs: pids}, nil
}

// Remove forgets an account and deletes its stored credentials. It prompts on
// Stdout/Stdin unless assumeYes. removed reports whether a removal happened: a
// declined prompt prints "Cancelled" and returns (false, nil).
func (s *Switcher) Remove(identifier string, assumeYes bool) (removed bool, err error) {
	slot, err := transfer.ResolveSlot(s.st, identifier)
	if err != nil {
		return false, err
	}
	if slot.Number == s.CurrentAccountNumber() {
		fmt.Fprintln(s.out, printer.Yellowed(fmt.Sprintf("Warning: Codex account %s (%s) is currently active", slot.Number, slot.Email)))
	}
	if !assumeYes {
		fmt.Fprintf(s.out, "Are you sure you want to permanently remove Codex account %s (%s)? [y/N] ", slot.Number, slot.Email)
		line, _ := bufio.NewReader(s.in).ReadString('\n')
		if strings.ToLower(strings.TrimRight(line, "\r\n")) != "y" {
			fmt.Fprintln(s.out, printer.Dimmed("Cancelled"))
			return false, nil
		}
	}
	return s.st.RemoveSlot(slot.AccountKey)
}

// Alias sets an account's alias and returns (resolved number, normalized alias).
func (s *Switcher) Alias(identifier, alias string) (string, string, error) {
	slot, err := transfer.ResolveSlot(s.st, identifier)
	if err != nil {
		return "", "", err
	}
	normalized, err := NormalizeAlias(alias)
	if err != nil {
		return "", "", err
	}
	if err := s.st.SetAlias(slot.AccountKey, normalized); err != nil {
		return "", "", err
	}
	return slot.Number, normalized, nil
}

// UnsetAlias drops an account's alias and returns the resolved number.
func (s *Switcher) UnsetAlias(identifier string) (string, error) {
	slot, err := transfer.ResolveSlot(s.st, identifier)
	if err != nil {
		return "", err
	}
	return slot.Number, s.st.SetAlias(slot.AccountKey, "")
}

// SetAccountDisabled holds an account out of (or returns it to) automatic
// rotation and returns the resolved number.
func (s *Switcher) SetAccountDisabled(identifier string, disabled bool) (string, error) {
	slot, err := transfer.ResolveSlot(s.st, identifier)
	if err != nil {
		return "", err
	}
	return slot.Number, s.st.SetDisabled(slot.AccountKey, disabled)
}

// SwitchableAccountNumbers lists slots eligible for automatic rotation.
// API-key accounts are excluded unconditionally, unlike the Claude side's
// includeApiKeyAccounts: a Codex API-key login reports no usage at all, so
// there is nothing for a threshold to mean.
func (s *Switcher) SwitchableAccountNumbers() []string {
	out := []string{}
	for _, sl := range s.st.Slots() {
		if !sl.Disabled && sl.AuthMode != "apikey" {
			out = append(out, sl.Number)
		}
	}
	return out
}

// AccountNumbers lists every managed slot number, rotation-eligible or not.
func (s *Switcher) AccountNumbers() []string {
	out := []string{}
	for _, sl := range s.st.Slots() {
		out = append(out, sl.Number)
	}
	return out
}

// Status is the active account's status. Slot is nil when no managed account
// is live.
type Status struct {
	Slot         *store.Slot
	Usage        usage.UsageEntry
	TotalManaged int
}

// Status gathers the active account's status, fetching usage for any account
// that is due (switcher.py's fetch=None).
func (s *Switcher) Status(ctx context.Context) Status {
	number := s.CurrentAccountNumber()
	slots := s.st.Slots()
	if number == "" {
		return Status{TotalManaged: len(slots)}
	}
	entries := s.usageFor(ctx, slots, nil, number)
	slots = s.st.Slots()
	for i := range slots {
		if slots[i].Number == number {
			sl := slots[i]
			return Status{Slot: &sl, Usage: entries[number], TotalManaged: len(slots)}
		}
	}
	return Status{TotalManaged: len(slots)}
}

// JSON is the `tycswap codex status --json` document.
func (st Status) JSON() map[string]any {
	if st.Slot == nil {
		return map[string]any{"schemaVersion": jsonout.SchemaVersion, "provider": ProviderID, "active": nil}
	}
	state, u := jsonout.UsageFields(st.Usage.DecisionValue())
	number, _ := strconv.Atoi(st.Slot.Number)
	var usageVal any
	if u != nil {
		usageVal = u
	}
	return map[string]any{
		"schemaVersion": jsonout.SchemaVersion,
		"provider":      ProviderID,
		"active": map[string]any{
			"number":      number,
			"email":       st.Slot.Email,
			"workspace":   st.Slot.WorkspaceName,
			"alias":       st.Slot.Alias,
			"plan":        st.Slot.Plan,
			"managed":     true,
			"usageStatus": state,
			"usage":       usageVal,
		},
		"totalManagedAccounts": st.TotalManaged,
	}
}

// Render prints the human status block.
func (st Status) Render(w io.Writer) {
	if st.Slot == nil {
		fmt.Fprintf(w, "%s %s\n", printer.Bolded("Status:"), printer.Dimmed("No active Codex account"))
		return
	}
	ws := st.Slot.WorkspaceName
	if ws == "" {
		ws = "personal"
	}
	fmt.Fprintf(w, "%s %s (%s %s)\n", printer.Bolded("Status:"), printer.Accent("Codex-"+st.Slot.Number),
		st.Slot.Email, printer.Muted("["+ws+"]"))
	fmt.Fprintf(w, "  %s\n", printer.Dimmed(fmt.Sprintf("Total managed Codex accounts: %d", st.TotalManaged)))
	for _, line := range usageLines(st.Usage) {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

// usageLines renders one entry's usage. The Claude side's renderer
// (reporting.usageEntryLines) is unexported, so this mirrors its shape: a
// sentinel replaces the bars, otherwise one line per reported window.
func usageLines(e usage.UsageEntry) []string {
	if e.Sentinel != "" {
		return []string{printer.Dimmed("Usage: " + e.Sentinel)}
	}
	if e.LastGood == nil {
		return []string{printer.Dimmed("Usage: unavailable")}
	}
	var out []string
	for _, w := range []struct{ key, label string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		win, ok := e.LastGood[w.key].(map[string]any)
		if !ok {
			continue
		}
		pct, ok := toFloat(win["pct"])
		if !ok {
			continue
		}
		line := fmt.Sprintf("%s: %.0f%%", w.label, pct)
		if cd, _ := win["countdown"].(string); cd != "" {
			line += " " + printer.Muted("(resets in "+cd+")")
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{printer.Dimmed("Usage: unavailable")}
	}
	return out
}

// TokenStatus reports diagnostics for one slot's stored token. It never
// returns token material — only derived facts.
func (s *Switcher) TokenStatus(identifier string) (map[string]any, error) {
	slot, err := transfer.ResolveSlot(s.st, identifier)
	if err != nil {
		return nil, err
	}
	payload := s.st.ReadSnapshot(slot.AccountKey)
	if payload == nil {
		return map[string]any{"number": slot.Number, "state": "no credentials"}, nil
	}
	if slot.AuthMode == "apikey" {
		return map[string]any{"number": slot.Number, "state": "api key"}, nil
	}
	tokens, _ := payload["tokens"].(map[string]any)
	rt, _ := tokens["refresh_token"].(string)
	now := clock.Seconds(s.clk)
	var expiresAt, expiresIn any
	if exp := authfile.AccessTokenExpiry(payload); exp != nil {
		expiresAt, expiresIn = *exp, *exp-now
	}
	return map[string]any{
		"number":           slot.Number,
		"state":            "oauth",
		"expiresAt":        expiresAt,
		"expiresInSeconds": expiresIn,
		"refreshDue":       s.needsRefresh(payload, now),
		"hasRefreshToken":  rt != "",
		"lastRefresh":      payload["last_refresh"],
	}, nil
}

func (s *Switcher) renumber(mapping map[string]string) error {
	return s.withLock(func() error { return s.st.Renumber(mapping) })
}

// Swap exchanges two accounts' slot numbers. Only sequence.json changes:
// snapshots are keyed by account key, so renumbering never moves a secret.
func (s *Switcher) Swap(first, second string) (string, string, error) {
	a, err := transfer.ResolveSlot(s.st, first)
	if err != nil {
		return "", "", err
	}
	b, err := transfer.ResolveSlot(s.st, second)
	if err != nil {
		return "", "", err
	}
	if a.Number == b.Number {
		return "", "", cerr.Switch("Cannot swap an account with itself")
	}
	if err := s.renumber(map[string]string{a.AccountKey: b.Number, b.AccountKey: a.Number}); err != nil {
		return "", "", err
	}
	return a.Number, b.Number, nil
}

// Move gives an account a specific slot number, swapping with any occupant.
// It returns (from, to, swapped).
func (s *Switcher) Move(account, target string) (string, string, bool, error) {
	slot, err := transfer.ResolveSlot(s.st, account)
	if err != nil {
		return "", "", false, err
	}
	target = strings.TrimSpace(target)
	if n, err := strconv.Atoi(target); err != nil || !isDigits(target) || n < 1 {
		return "", "", false, cerr.Switch("'%s' is not a valid slot number", target)
	}
	if slot.Number == target {
		return slot.Number, target, false, nil
	}
	mapping := map[string]string{slot.AccountKey: target}
	swapped := false
	for _, o := range s.st.Slots() {
		if o.Number == target {
			mapping[o.AccountKey] = slot.Number
			swapped = true
			break
		}
	}
	if err := s.renumber(mapping); err != nil {
		return "", "", false, err
	}
	return slot.Number, target, swapped, nil
}

// Rotate switches to the next rotatable account after the active one, or to
// the first when the active one is not rotatable.
func (s *Switcher) Rotate(ctx context.Context) (SwitchResult, error) {
	candidates := s.SwitchableAccountNumbers()
	if len(candidates) == 0 {
		return SwitchResult{}, cerr.Switch("No rotatable Codex accounts")
	}
	active := s.CurrentAccountNumber()
	idx := -1
	for i, c := range candidates {
		if c == active {
			idx = i
		}
	}
	if active == "" || idx < 0 {
		return s.SwitchTo(ctx, candidates[0])
	}
	if len(candidates) == 1 {
		return SwitchResult{}, cerr.Switch("Only one rotatable Codex account — nothing to rotate to")
	}
	return s.SwitchTo(ctx, candidates[(idx+1)%len(candidates)])
}

// SwitchBest switches to whichever rotatable account has the most headroom.
func (s *Switcher) SwitchBest(ctx context.Context) (SwitchResult, error) {
	snap := s.AccountsSnapshot(ctx, nil)
	rotatable := map[string]bool{}
	for _, n := range s.SwitchableAccountNumbers() {
		rotatable[n] = true
	}
	active := s.CurrentAccountNumber()
	best := ""
	var bestPct *float64
	for _, a := range snap.Accounts {
		if !rotatable[a.Number] || a.Number == active {
			continue
		}
		pct := BindingPct(a.Usage.LastGood)
		if pct == nil {
			continue
		}
		if bestPct == nil || *pct < *bestPct {
			best, bestPct = a.Number, pct
		}
	}
	if best == "" {
		return SwitchResult{}, cerr.Switch("No Codex account with a known measurement to switch to")
	}
	return s.SwitchTo(ctx, best)
}

// BindingPct is the utilization that decides an account's fate: the worst of
// the five_hour and seven_day windows it reports, or nil without a
// measurement (codex/autoswitch.py binding_pct; lives here so SwitchBest and
// the autoswitch package share one rule without an import cycle). Values read
// back from the usage store are json.Number, so every numeric form is accepted.
func BindingPct(u map[string]any) *float64 {
	if u == nil {
		return nil
	}
	var best *float64
	for _, key := range []string{"five_hour", "seven_day"} {
		win, ok := u[key].(map[string]any)
		if !ok {
			continue
		}
		v, ok := toFloat(win["pct"])
		if !ok {
			continue
		}
		if best == nil || v > *best {
			vv := v
			best = &vv
		}
	}
	return best
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
