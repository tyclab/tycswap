package autoswitch

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

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
}

type StartSnapshot struct {
	Current       string
	CurrentMarked bool
	Marked        []string
	Candidates    []string
	Entries       map[string]usage.UsageEntry
	Settings      settings.AutoSwitchSettings
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
	e.lockPath = filepath.Join(filepath.Dir(e.statePath), ".autoswitch_state.lock")
	snap, err := snapshot()
	if err != nil {
		return startFailure("selection-failed", err)
	}
	if outcome, stop := startNoop(snap, account); stop {
		return outcome
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
	ordered := StartTargets(candidates, snap.Entries, snap.Settings, e.models)
	var failures []string
	for _, target := range ordered {
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
		outcome := StartOutcome{}
		err := filelock.New(e.lockPath, 0).With(func() error {
			current, err := snapshot()
			if err != nil {
				return err
			}
			if noOp, stop := startNoop(current, account); stop {
				outcome = noOp
				return nil
			}
			state := e.readState()
			if !containsStartCandidate(current.Candidates, target) || quarantinedSet(state)[target] ||
				len(StartTargets([]string{target}, current.Entries, current.Settings, settings.ParseModelNames(current.Settings.Model))) == 0 {
				outcome = startFailure("selection-changed", fmt.Errorf("start login eligibility changed; continuing with the current login"))
				return nil
			}
			result, err := sw.SwitchTo(target, true)
			if err != nil {
				return err
			}
			if result == nil || !truthy(result["switched"]) {
				outcome = StartOutcome{Reason: "already-active"}
				return nil
			}
			outcome = StartOutcome{Switched: true, From: current.Current, To: target,
				Detail: "Switched to start login Account-" + target + "."}
			state["schemaVersion"] = StateSchemaVersion
			state["lastSwitchAt"], state["lastSwitchTo"] = e.nowSeconds(), target
			clearActiveMemory(state)
			if err := e.writeState(state); err != nil {
				outcome.Reason = "stamp-failed"
				outcome.Detail += " Could not record the auto-switch cooldown: " + err.Error()
			}
			return nil
		})
		if err != nil {
			return startFailure("switch-failed", err)
		}
		return outcome
	}
	detail := "no usable start login; continuing with the current login"
	if len(failures) > 0 {
		detail += " (" + strings.Join(failures, "; ") + ")"
	}
	return StartOutcome{Reason: "none-usable", Detail: detail}
}

func startNoop(snap StartSnapshot, account string) (StartOutcome, bool) {
	switch {
	case account != "" && snap.Current == account:
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

// StartTargets ranks cached candidates by tier and binding utilization; stable ties keep sequence order.
func StartTargets(candidates []string, entries map[string]usage.UsageEntry, set settings.AutoSwitchSettings, models []string) []string {
	values := make(map[string]any, len(entries))
	for num, entry := range entries {
		values[num] = entry.DecisionValue()
	}
	required := requiredModels(models, values)
	type candidate struct {
		num   string
		tier  int
		known bool
		room  float64
	}
	var ranked []candidate
	for _, num := range candidates {
		if entries[num].TokenDead() || !hasRequiredModels(values[num], required) {
			continue
		}
		h := usage.AccountHeadroomByClass(usageDict(values[num]), models)
		room := h.Binding()
		if room != nil && *room <= 0 {
			continue
		}
		row := candidate{num: num, tier: 1, known: room != nil}
		if room != nil {
			row.room = *room
			if belowEveryBar(h, set) {
				row.tier = 0
			}
		}
		ranked = append(ranked, row)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.known != b.known {
			return a.known
		}
		return a.room > b.room
	})
	result := make([]string, len(ranked))
	for i, row := range ranked {
		result[i] = row.num
	}
	return result
}
