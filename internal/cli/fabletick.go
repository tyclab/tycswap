// The Fable store's tick (DESIGN A102): `auto --once` runs one engine per
// store, the Fable one over an adapter that answers for the Fable scope.
package cli

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/usage"
)

// fableAdapter is the Fable store's autoswitch.Switcher: the roster, the
// identities and the usage are the default store's, the logins, the live
// account, the switch and the poll scope the Fable store's. It has none of
// the engine's ownership hooks: no profile or claim owns a Fable login.
type fableAdapter struct {
	def   *core.Switcher
	fable *store.Store
	log   *logging.Logger
}

var _ autoswitch.Switcher = fableAdapter{}

func newFableAdapter(def *core.Switcher, log *logging.Logger) (fableAdapter, error) {
	f, err := def.Store.ForFable()
	if err != nil {
		return fableAdapter{}, err
	}
	return fableAdapter{def: def, fable: f, log: log}, nil
}

func (a fableAdapter) CurrentAccountNumber() *string {
	data, _ := a.def.ReadSequence()
	if slot := a.def.Store.ResolveFableLiveSlot(data).Slot; slot != "" {
		return &slot
	}
	return nil
}

func (a fableAdapter) HasLiveLogin() bool                      { return a.fable.HasLiveLogin() }
func (a fableAdapter) AccountEmail(num string) string          { return a.def.AccountEmail(num) }
func (a fableAdapter) SwitchableAccountNumbers() []string      { return reporting.FablePool(a.def.Store) }
func (a fableAdapter) AccountKindFor(num string) string        { return a.def.AccountKindFor(num) }
func (a fableAdapter) BackfillAccountUUID(num, uuid string)    { a.def.BackfillAccountUUID(num, uuid) }
func (a fableAdapter) LiveSessionPidsFor(string, string) []int { return nil }
func (a fableAdapter) BackupDir() string                       { return a.def.BackupDir() }

func (a fableAdapter) AccountIdentity(num string) map[string]string {
	return a.def.AccountIdentity(num)
}

func (a fableAdapter) ReadAccountCredentials(num, email string) string {
	v, _ := a.fable.ReadAccountCredentials(num, email)
	return v
}

func (a fableAdapter) PersistBackupCredentials(num, email, creds string) error {
	return a.fable.Lock.With(func() error { return a.fable.WriteAccountCredentials(num, email, creds) })
}

func (a fableAdapter) RefreshBackupGuarded(ctx context.Context, c oauth.Client, num, email, held string) oauth.RefreshOutcome {
	return a.def.Store.RefreshFableBackupGuarded(ctx, c, num, email, held)
}

// UsageEntriesByAccount is the default store's collector: one fetch path,
// with the default login, serves both stores.
func (a fableAdapter) UsageEntriesByAccount(fetch map[string]bool) map[string]usage.UsageEntry {
	return reporting.UsageEntriesByAccount(a.def.Store, fetch)
}

func (a fableAdapter) SwitchTo(num string, jsonOut bool) (map[string]any, error) {
	return a.def.EngineSwitchFable(num, jsonOut)
}

func (a fableAdapter) SetPollPolicyInputs(threshold float64, models []string) {
	if reporting.SetScopedPollInputs(a.BackupDir(), a.fable.ScopeRoot(), threshold, models) {
		reporting.ReplanCachedUsage(a.def.Store)
	}
}

func (a fableAdapter) ClearPollPolicyInputs() {
	reporting.ClearScopedPollInputs(a.BackupDir(), a.fable.ScopeRoot())
}

// RecordedSwitch is the Fable state file's switch stamp: every tycswap
// switch of the Fable store writes it, by hand or the engine's, and nothing
// else does. The Fable chain record is no such proof: the resolver writes it
// for a login the profile endpoint names, one made elsewhere included.
func (a fableAdapter) RecordedSwitch() (string, float64) {
	return autoswitch.ReadSwitchStamp(a.fable.StatePath())
}

// LogSwitch is the engine's switch line, naming the store.
func (a fableAdapter) LogSwitch(number, actor string) {
	a.log.Action("Switched the Fable store to account "+number, actor)
}

// fableEvent is a Fable tick's event: its JSON carries "store":"fable" and
// its line names the store.
type fableEvent struct{ autoswitch.Event }

func (e fableEvent) JSON() map[string]any {
	v := e.Event.JSON()
	v["store"] = "fable"
	return v
}

// Human names the store; a quarantined Fable login is signed in again with
// add --login --lineage fable, not re-added as a default login.
func (e fableEvent) Human() string {
	if q, ok := e.Event.(autoswitch.QuarantineEvent); ok {
		return "Fable store: Account-" + q.Number + " (" + q.Email + ") quarantined: " + q.Reason +
			". Sign its Fable login in again with 'tycswap add --login --lineage fable --slot " + q.Number + "'."
	}
	return "Fable store: " + e.Event.Human()
}

// fableTick runs the Fable store's tick after the default one, while the
// store is on; ran is false when it does not run. The engine counts the Fable
// windows the accounts report, always, with no reservation; a real tick
// harvests the store's rotated login first and keeps one idle login alive
// after.
func fableTick(sw *core.Switcher, merged settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool) (outcome autoswitch.TickOutcome, ran bool) {
	if !sw.Store.FableStore().On {
		return autoswitch.NoAction, false
	}
	if !dryRun {
		sw.Store.HarvestFableLive()
	}
	adapter, err := newFableAdapter(engineSwitcher(sw, dryRun), sw.Log)
	if err != nil {
		return autoswitch.NoAction, false
	}
	emit := func(ev autoswitch.Event) { onEvent(fableEvent{ev}) }
	s := merged
	s.Model = nil
	names := reporting.FableWindowNames(adapter.def.Store, sw.Clk.Now())
	if len(names) > 0 {
		model := strings.Join(names, ",")
		s.Model = &model
	}
	moved := false
	if !dryRun && autoSwitchOn(sw.BackupDir()) {
		var on bool
		if moved, on = checkFableLive(adapter, sw, s, names, emit); !on {
			return autoswitch.Blocked, true
		}
	}
	engine := autoswitch.NewEngine(adapter, s, emit, dryRun,
		autoswitch.WithStatePath(adapter.fable.StatePath()),
		autoswitch.WithOAuthClient(sw.OAuth),
		autoswitch.WithLogger(sw.Log.Prefixed("Fable store: ")),
		autoswitch.WithClock(sw.Clk),
		autoswitch.WithEnabled(func() bool { return autoSwitchOn(sw.BackupDir()) }),
	)
	outcome = engine.Tick()
	if moved {
		outcome = significantOutcome(autoswitch.Switched, outcome)
	}
	if !dryRun {
		keepFableLoginAlive(adapter, sw, emit)
	}
	return outcome, true
}

// checkFableLive checks the Fable store's live login itself (DESIGN A102):
// its usage is the default login's, so a dead one shows nowhere else. An
// expired login is refreshed in the store under its Claude Code locks; a
// refresh the server rejects quarantines it in the Fable state file. A live
// login quarantined so moves the store to the best account with a usable
// Fable login, or empties it: on is false then. The default login is never
// read or written.
func checkFableLive(a fableAdapter, sw *core.Switcher, s settings.AutoSwitchSettings, models []string, emit func(autoswitch.Event)) (moved, on bool) {
	data, _ := a.def.ReadSequence()
	slot := a.def.Store.ResolveFableLiveSlot(data).Slot
	live, _, err := a.fable.Creds.ReadActive()
	if slot == "" || err != nil || live == "" {
		return false, true
	}
	email := a.AccountEmail(slot)
	if a.def.Store.FableStore().Usable {
		if !oauth.IsOAuthTokenExpired(oauth.ExtractOAuthData(live)["expiresAt"], sw.Clk.Now()) {
			return false, true
		}
		out := a.def.Store.RefreshFableLiveGuarded(context.Background(), sw.OAuth, slot, email, live)
		if out.Error != oauth.ErrInvalidGrant && out.Error != oauth.ErrNoRefreshToken {
			return false, true
		}
		if err := autoswitch.Quarantine(a.fable.StatePath(), sw.Clk, slot, email, oauth.ErrInvalidGrant, live, emit); err != nil {
			sw.Log.Warningf("Fable store: the dead live Fable login of account %s was not quarantined: %v", slot, err)
			return false, true
		}
	}
	return leaveDeadFableLogin(a, sw, slot, email, s, models, emit)
}

// leaveDeadFableLogin moves the Fable store off account dead's quarantined
// login onto the account the cached usage ranks first among those with a
// usable Fable login, else the first of them not known to be at a limit, else
// the first; with none it empties the store.
func leaveDeadFableLogin(a fableAdapter, sw *core.Switcher, dead, email string, s settings.AutoSwitchSettings, models []string, emit func(autoswitch.Event)) (moved, on bool) {
	var pool []string
	for _, num := range a.SwitchableAccountNumbers() {
		if num != dead {
			pool = append(pool, num)
		}
	}
	ts := sw.Clk.Now().UTC().Format("2006-01-02T15:04:05") + "Z"
	warn := func(err error) { sw.Log.Warningf("Fable store: the switch off a dead login was not recorded: %v", err) }
	// Turned off since the tick began, or moved off the dead login by another
	// tick, the store stays as it is: both are re-read under the state lock,
	// as the engine's own switch re-reads its state (A83).
	stay := false
	stays := func() bool {
		data, _ := a.def.ReadSequence()
		stay = !autoSwitchOn(sw.BackupDir()) || a.def.Store.ResolveFableLiveSlot(data).Slot != dead
		return stay
	}
	if len(pool) == 0 {
		emptied := false
		held, err := autoswitch.EngineSwitch(a.fable.StatePath(), sw.Clk, s, warn, func() (string, error) {
			if stays() {
				return "", nil
			}
			_, err := switching.EmptyFable(a.fable)
			emptied = err == nil
			return "", err
		})
		if stay {
			return false, true
		}
		if !emptied {
			fableHeldOrFailed(sw, emit, ts, held, "could not empty the store off the dead Fable login of account "+dead, err)
			return false, true
		}
		sw.Log.Action("Emptied the Fable store", "engine:failover")
		emit(autoswitch.NoSwitchEvent{Ts: ts, Reason: "no-viable-target",
			Detail: "Account-" + dead + "'s Fable login is dead and no other account has a usable one; the Fable store is empty, so Fable sessions start on the default login"})
		return false, false
	}
	entries, now := a.UsageEntriesByAccount(map[string]bool{}), sw.Clk.Now()
	ranked := append(autoswitch.StartTargets(pool, entries, s, models, now), autoswitch.NotKnownAtALimit(pool, entries, models, now)...)
	target := append(ranked, pool...)[0]
	switched := false
	held, err := autoswitch.EngineSwitch(a.fable.StatePath(), sw.Clk, s, warn, func() (string, error) {
		if stays() {
			return "", nil
		}
		r, err := a.SwitchTo(target, true)
		if err != nil {
			return "", err
		}
		num, ok := jsonout.SwitchedTo(r)
		switched = ok
		return num, nil
	})
	if stay {
		return false, true
	}
	if !switched {
		fableHeldOrFailed(sw, emit, ts, held, "could not move the store off the dead Fable login of account "+dead+" to account "+target, err)
		return false, true
	}
	a.LogSwitch(target, "engine:failover")
	emit(autoswitch.SwitchEvent{Ts: ts, Trigger: "failover",
		FromRef: map[string]any{"number": slotNumber(dead), "email": email},
		ToRef:   map[string]any{"number": slotNumber(target), "email": a.AccountEmail(target)}})
	return true, true
}

// fableHeldOrFailed reports a move off a dead login that a switch by hand
// held (a cooldown event, as a tick has) or that failed (a warning).
func fableHeldOrFailed(sw *core.Switcher, emit func(autoswitch.Event), ts, held, what string, err error) {
	if held != "" {
		emit(autoswitch.NoSwitchEvent{Ts: ts, Reason: "cooldown", Detail: held})
		return
	}
	sw.Log.Warningf("Fable store: %s: %v", what, err)
}

func slotNumber(slot string) any {
	if n, err := strconv.Atoi(slot); err == nil {
		return n
	}
	return slot
}

// significantOutcome is the more significant of two ticks' outcomes: an
// error, then a switch, then a block, then no action.
func significantOutcome(a, b autoswitch.TickOutcome) autoswitch.TickOutcome {
	rank := map[autoswitch.TickOutcome]int{autoswitch.Error: 0, autoswitch.Switched: 1, autoswitch.Blocked: 2, autoswitch.NoAction: 3}
	if rank[b] < rank[a] {
		return b
	}
	return a
}

// fableIdleAfter is half the 28-day refresh-token life the pilot measured;
// fableTokenLife is its measured access-token life, since a credential
// carries only its expiry.
const (
	fableIdleAfter = 14 * 24 * time.Hour
	fableTokenLife = 8 * time.Hour
)

// keepFableLoginAlive refreshes the idle Fable login refreshed longest ago,
// more than fableIdleAfter ago, so its refresh token does not expire unused.
// The live login is Claude Code's to refresh; a quarantined one is left.
// invalid_grant quarantines it in the Fable state file.
func keepFableLoginAlive(a fableAdapter, sw *core.Switcher, emit func(autoswitch.Event)) {
	data, _ := a.def.ReadSequence()
	if data == nil {
		return
	}
	live := a.def.Store.ResolveFableLiveSlot(data).Slot
	held := autoswitch.ReadQuarantineEntries(a.fable.StatePath())
	now := sw.Clk.Now()
	var pick, email, creds string
	var oldest time.Time
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if _, dead := held[num]; dead || num == live {
			continue
		}
		e := a.AccountEmail(num)
		c := a.ReadAccountCredentials(num, e)
		ms, ok := oauth.ExpiresAtMS(c)
		at := time.UnixMilli(int64(ms)).Add(-fableTokenLife)
		if !ok || now.Sub(at) <= fableIdleAfter || pick != "" && !at.Before(oldest) {
			continue
		}
		pick, email, creds, oldest = num, e, c, at
	}
	if pick == "" {
		return
	}
	out := a.RefreshBackupGuarded(context.Background(), sw.OAuth, pick, email, creds)
	switch {
	case out.Error == "" && out.Credentials != "":
		sw.Log.Infof("Kept the Fable login of account %s alive", pick)
	case out.Error == oauth.ErrInvalidGrant || out.Error == oauth.ErrNoRefreshToken:
		if err := autoswitch.Quarantine(a.fable.StatePath(), sw.Clk, pick, email, oauth.ErrInvalidGrant, creds, emit); err != nil {
			sw.Log.Warningf("Fable store: the dead Fable login of account %s was not quarantined: %v", pick, err)
		}
	default:
		sw.Log.Debugf("Fable store: the Fable login of account %s was not kept alive: %s", pick, out.Error)
	}
}
