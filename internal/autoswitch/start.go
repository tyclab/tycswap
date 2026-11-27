package autoswitch

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

type StartOutcome struct {
	Switched bool
	From, To string
	Reason   string
	Detail   string
	// Hold is the token of the start hold recorded for the session, "" for none (DESIGN A105).
	Hold string
}

type StartSnapshot struct {
	Current         string
	CurrentDisabled bool
	IdleOpus        bool
	ReservedLast    func(string) bool
	CurrentMarked   bool
	Marked          []string
	Candidates      []string
	Entries         map[string]usage.UsageEntry
	Settings        settings.AutoSwitchSettings
	// Set only for an already pending, idle native Fable selection.
	FableAllocationExceptions map[string]bool
}

// StartSelection prepares outside the state lock, then shares the engine's switch/stamp order.
func StartSelection(sw Switcher, account string, snapshot func() (StartSnapshot, error), opts ...Option) StartOutcome {
	e := &Engine{sw: sw, clk: clock.System{}, oauth: oauth.NewHTTPClient(), onEvent: func(Event) {}}
	for _, opt := range opts {
		opt(e)
	}
	if e.statePath == "" {
		e.statePath = StatePath(sw.BackupDir())
	}
	e.lockPath = stateLockPath(e.statePath)
	snap, err := snapshot()
	if err != nil {
		return startFailure("selection-failed", err)
	}
	if outcome, stop := startNoop(snap, account); stop {
		if !startHoldWanted(snap, snap.Current, e.clk.Now()) {
			return outcome
		}
		return e.settleStart(snapshot, account, "", false)
	}
	e.settings.Store(&snap.Settings)
	e.models = settings.ParseModelNames(snap.Settings.Model)
	state := e.readState()
	quarantined := quarantinedSet(state)
	var candidates []string
	for _, num := range snap.Candidates {
		if !quarantined[num] {
			candidates = append(candidates, num)
		}
	}
	var failures []string
	for _, row := range snap.rank(candidates, e.models, e.clk.Now()) {
		target := row.num
		email := sw.AccountEmail(target)
		preparationDetail := ""
		e.onEvent = func(event Event) {
			if notice, ok := event.(NoSwitchEvent); ok {
				preparationDetail = notice.Detail
			}
		}
		status := e.freshenTarget(target, email)
		switch status {
		case "invalid_grant", "identity-conflict":
			if err := e.quarantine(target, email, status); err != nil {
				return startFailure("quarantine-failed", err)
			}
			message := "login expired or refresh token rejected; renew it with tycswap add --login"
			if status == "identity-conflict" {
				message = "refreshed credential belongs to a different identity; renew it with tycswap add --login"
			}
			failures = append(failures, "Account-"+target+": "+message)
			continue
		case "ok":
		default:
			message := "could not refresh the login; check the connection or renew it with tycswap add --login"
			if status == "skip-live-session" {
				message = "a live session owns this login; exit that session before selecting it"
			} else if status == "ownership-blocked" {
				message = "could not safely release this login from its session profile"
				if preparationDetail != "" {
					message += ": " + preparationDetail
				}
			}
			failures = append(failures, "Account-"+target+": "+message)
			continue
		}
		return e.settleStart(snapshot, account, target, snap.hasIdlePreference())
	}
	detail := "no usable start login; continuing with the current login"
	if len(failures) > 0 {
		detail += " (" + strings.Join(failures, "; ") + ")"
	}
	return StartOutcome{Reason: "none-usable", Detail: detail}
}

var errSelectionChanged = errors.New("start login eligibility changed; continuing with the current login")

// settleStart re-reads the snapshot under the state lock and settles there:
// no switch when none is needed, else the switch to target, prepared outside
// the lock ("" for none). A session on a login at a limit gets a start hold
// while auto-switch is on, in the same write as the switch's stamp (DESIGN A105).
func (e *Engine) settleStart(snapshot func() (StartSnapshot, error), account, target string, preparedForIdle bool) StartOutcome {
	outcome := StartOutcome{}
	err := filelock.New(e.lockPath, 0).With(func() error {
		current, err := snapshot()
		if err != nil {
			return err
		}
		now := e.clk.Now()
		if noOp, stop := startNoop(current, account); stop {
			outcome = noOp
			if startHoldWanted(current, current.Current, now) {
				e.recordStartHold(&outcome, e.readState())
			}
			return nil
		}
		state := e.readState()
		if (preparedForIdle && !current.hasIdlePreference()) || !containsStartCandidate(current.Candidates, target) || quarantinedSet(state)[target] {
			outcome = startFailure("selection-changed", errSelectionChanged)
			return nil
		}
		ranked := current.rank([]string{target}, settings.ParseModelNames(current.Settings.Model), now)
		if len(ranked) == 0 {
			outcome = startFailure("selection-changed", errSelectionChanged)
			return nil
		}
		hold := current.Settings.Enabled && ranked[0].tier == startAtLimit
		result, err := e.sw.SwitchTo(target, true)
		if err != nil {
			return err
		}
		if result == nil || !truthy(result["switched"]) {
			outcome = StartOutcome{Reason: "already-active"}
			if hold {
				e.recordStartHold(&outcome, state)
			}
			return nil
		}
		outcome = StartOutcome{Switched: true, From: current.Current, To: target,
			Detail: "Switched to start login Account-" + target + "."}
		if hold {
			outcome.Detail = "Switched to start login Account-" + target + ", which is at its limit; auto-switch moves on once the session is up."
		}
		stampSwitch(state, target, e.nowSeconds(), false)
		token := settleStartHolds(state, e.nowSeconds(), hold, e.holdToken)
		if err := e.writeState(state); err != nil {
			outcome.Reason = "stamp-failed"
			outcome.Detail += " Could not record the auto-switch cooldown: " + err.Error()
			return nil
		}
		outcome.Hold = token
		return nil
	})
	if err != nil {
		return startFailure("switch-failed", err)
	}
	return outcome
}

// recordStartHold writes a start hold for the session into state, under the
// state lock the caller holds; a failed write is said in the outcome.
func (e *Engine) recordStartHold(outcome *StartOutcome, state map[string]any) {
	state["schemaVersion"] = StateSchemaVersion
	token := settleStartHolds(state, e.nowSeconds(), true, e.holdToken)
	if err := e.writeState(state); err != nil {
		outcome.Detail = strings.TrimSpace(outcome.Detail + " Could not hold auto-switch for the starting session: " + err.Error())
		return
	}
	outcome.Hold = token
}

// startHoldWanted reports whether a session starting on num gets a start
// hold: auto-switch is on and num is at a limit by start's ranking.
func startHoldWanted(snap StartSnapshot, num string, now time.Time) bool {
	if !snap.Settings.Enabled || num == "" {
		return false
	}
	ranked := rankStartTargets([]string{num}, snap.Entries, snap.Settings, settings.ParseModelNames(snap.Settings.Model), now)
	return len(ranked) == 1 && ranked[0].tier == startAtLimit
}

// StartLoginHasRoom reports whether num ranks below its limits by start's
// ranking: a session can start on it and stay (DESIGN A122).
func StartLoginHasRoom(snap StartSnapshot, num string, now time.Time) bool {
	ranked := rankStartTargets([]string{num}, snap.Entries, snap.Settings, settings.ParseModelNames(snap.Settings.Model), now)
	return len(ranked) == 1 && ranked[0].tier != startAtLimit
}

// StartKeepsLogin reports whether a start without an account neither switches
// nor holds the login snap describes (DESIGN A125).
func StartKeepsLogin(snap StartSnapshot, now time.Time) bool {
	_, stop := startNoop(snap, "")
	return stop && !startHoldWanted(snap, snap.Current, now)
}

func startNoop(snap StartSnapshot, account string) (StartOutcome, bool) {
	switch {
	case account != "" && snap.Current == account && !snap.CurrentDisabled:
		return StartOutcome{Reason: "already-active", Detail: "Requested start login is already active."}, true
	case account == "" && len(snap.Marked) == 0:
		return StartOutcome{Reason: "no-start-login"}, true
	case account == "" && snap.CurrentMarked:
		return StartOutcome{Reason: "already-start-login"}, true
	case account == "" && !snap.Settings.Enabled:
		return StartOutcome{Reason: "auto-off"}, true
	}
	return StartOutcome{}, false
}

func startFailure(reason string, err error) StartOutcome {
	return StartOutcome{Reason: reason, Detail: "Could not select a start login: " + err.Error() + "; continuing with the current login."}
}

func containsStartCandidate(candidates []string, target string) bool {
	for _, num := range candidates {
		if num == target {
			return true
		}
	}
	return false
}

// Start's tiers: below every counted bar, room (unknown, or over a bar and
// below 100 %), at a limit (DESIGN A105).
const (
	startBelowBars = iota
	startRoom
	startAtLimit
)

type startTarget struct {
	num          string
	tier         int
	known        bool
	room         float64
	reset        *float64
	spentPremium bool
}

// rankStartTargets ranks cached candidates by tier and binding utilization,
// read as of now like the engine's (DESIGN A91); stable ties keep sequence
// order, which alone orders the logins at a limit.
func rankStartTargets(candidates []string, entries map[string]usage.UsageEntry, set settings.AutoSwitchSettings, models []string, now time.Time) []startTarget {
	return orderStartTargets(candidates, entries, set, models, now, startOrder{})
}

type startOrder struct {
	soonestBinding, idleOpus bool
	family                   string
}

func (s StartSnapshot) hasIdlePreference() bool {
	return s.IdleOpus || len(s.FableAllocationExceptions) > 0
}

func (s StartSnapshot) rank(candidates, models []string, now time.Time) []startTarget {
	order := startOrder{idleOpus: s.IdleOpus, soonestBinding: s.IdleOpus && s.Settings.Strategy == "soonest-reset"}
	ranked := orderStartTargets(candidates, s.Entries, s.Settings, models, now, order)
	if s.IdleOpus && s.ReservedLast != nil {
		ordinary := func(row startTarget) bool { return row.tier != startAtLimit && !s.ReservedLast(row.num) }
		sort.SliceStable(ranked, func(i, j int) bool { return ordinary(ranked[i]) && !ordinary(ranked[j]) })
	}
	if len(s.FableAllocationExceptions) > 0 {
		ordinary := func(row startTarget) bool { return row.tier != startAtLimit && !s.FableAllocationExceptions[row.num] }
		sort.SliceStable(ranked, func(i, j int) bool { return ordinary(ranked[i]) && !ordinary(ranked[j]) })
	}
	return ranked
}

func orderStartTargets(candidates []string, entries map[string]usage.UsageEntry, set settings.AutoSwitchSettings, models []string, now time.Time, order startOrder) []startTarget {
	values := decisionValuesAt(entries, now)
	required := requiredModels(models)
	var ranked []startTarget
	for _, num := range candidates {
		if entries[num].TokenDead() || !hasRequiredModels(values[num], required) {
			continue
		}
		value := usageDict(values[num])
		applicable := models
		spent := false
		if order.idleOpus {
			applicable, spent = opusQuota(value, models)
		} else if order.family != "" {
			applicable = oauth.FamilyWindows(oauth.NewUsage(value), order.family)
		}
		h := usage.AccountHeadroomByClass(value, applicable)
		room := h.Binding()
		row := startTarget{num: num, tier: startRoom, known: room != nil}
		if room != nil {
			row.room = *room
			row.spentPremium = spent
			if order.soonestBinding {
				row.reset = bindingWindowReset(value, applicable, *room)
			}
			if *room <= 0 {
				row.tier = startAtLimit
			} else if belowEveryBar(h, set) {
				row.tier = startBelowBars
			}
		}
		ranked = append(ranked, row)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.tier == startAtLimit {
			return false
		}
		if a.known != b.known {
			return a.known
		}
		if a.spentPremium != b.spentPremium {
			return a.spentPremium
		}
		if (a.reset == nil) != (b.reset == nil) {
			return a.reset != nil
		}
		if a.reset != nil && *a.reset != *b.reset {
			return *a.reset < *b.reset
		}
		return a.room > b.room
	})
	return ranked
}

// A below-threshold Opus session can use an earlier reset while both logins
// still have room. Require a known, strictly earlier binding reset, so ties
// and missing telemetry leave the current login alone.
func (e *Engine) earlierOpusTarget(current string, entries map[string]usage.UsageEntry, set settings.AutoSwitchSettings, quarantined map[string]bool) string {
	candidates := slices.DeleteFunc(slices.Clone(e.sw.SwitchableAccountNumbers()), func(num string) bool {
		return num != current && (quarantined[num] || e.isReserved(num) || !e.servesAccount(num) || e.sw.AccountKindFor(num) != "oauth")
	})
	rows := orderStartTargets(candidates, entries, set, e.models, e.clk.Now(), startOrder{soonestBinding: true, idleOpus: true})
	var source *startTarget
	for i := range rows {
		if rows[i].num == current {
			source = &rows[i]
			break
		}
	}
	if source == nil || source.reset == nil || source.tier != startBelowBars {
		return ""
	}
	for _, row := range rows {
		if row.tier == startBelowBars && row.reset != nil && *row.reset < *source.reset && (!source.spentPremium || row.spentPremium) {
			return row.num
		}
	}
	return ""
}

// A missing premium window provides no evidence its quota is spent.
func opusQuota(value map[string]any, models []string) ([]string, bool) {
	u := oauth.NewUsage(value)
	if u == nil {
		return models, false
	}
	models = slices.Clone(models)
	spent, hasRoom := false, false
	for _, window := range u.Scoped {
		name := strings.ToLower(window.Name)
		switch {
		case strings.Contains(name, "opus"):
			models = append(models, window.Name)
		case strings.Contains(name, "fable"), strings.Contains(name, "mythos"):
			spent = spent || window.Pct >= 100
			hasRoom = hasRoom || window.Pct < 100
		}
	}
	return models, spent && !hasRoom
}

// A tie in remaining room lasts until all equally binding windows have reset.
func bindingWindowReset(value map[string]any, models []string, room float64) *float64 {
	var latest *float64
	for _, window := range oauth.RelevantWindows(oauth.NewUsage(value), models) {
		if 100-window.Pct != room {
			continue
		}
		reset := parseResetTS(window.ResetsAt)
		if reset == nil {
			return nil
		}
		if latest == nil || *reset > *latest {
			latest = reset
		}
	}
	return latest
}

// StartTargets is start's ranking without the logins at a limit: the move
// off a dead live Fable login ranks its pool by it (DESIGN A102).
func StartTargets(candidates []string, entries map[string]usage.UsageEntry, set settings.AutoSwitchSettings, models []string, now time.Time) []string {
	return startTargetsWithRoom(rankStartTargets(candidates, entries, set, models, now))
}

// InitialPlacementTargets orders independently signed-in candidates for a new native session.
func InitialPlacementTargets(candidates []string, entries map[string]usage.UsageEntry, set settings.AutoSwitchSettings, family string, now time.Time) []string {
	return startTargetsWithRoom(initialPlacementRows(candidates, entries, set, family, now))
}

func KnownRoomTargets(candidates []string, entries map[string]usage.UsageEntry, set settings.AutoSwitchSettings, family string, now time.Time) []string {
	rows := initialPlacementRows(candidates, entries, set, family, now)
	return startTargetsWithRoom(slices.DeleteFunc(rows, func(r startTarget) bool { return !r.known }))
}

func initialPlacementRows(candidates []string, entries map[string]usage.UsageEntry, set settings.AutoSwitchSettings, family string, now time.Time) []startTarget {
	return orderStartTargets(candidates, entries, set, nil, now, startOrder{soonestBinding: set.Strategy == "soonest-reset", family: family})
}

func startTargetsWithRoom(rows []startTarget) []string {
	result := []string{}
	for _, row := range rows {
		if row.tier != startAtLimit {
			result = append(result, row.num)
		}
	}
	return result
}

// NotKnownAtALimit is candidates, in their order, less those whose cached
// usage, read as of now, shows a limit reached: the move off a dead live
// Fable login takes an account whose usage is not read yet before one known
// to be full when start's ranking names none (DESIGN A102).
func NotKnownAtALimit(candidates []string, entries map[string]usage.UsageEntry, models []string, now time.Time) []string {
	values := decisionValuesAt(entries, now)
	var out []string
	for _, num := range candidates {
		if room := usage.AccountHeadroomByClass(usageDict(values[num]), models).Binding(); room == nil || *room > 0 {
			out = append(out, num)
		}
	}
	return out
}
