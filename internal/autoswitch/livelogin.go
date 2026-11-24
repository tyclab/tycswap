package autoswitch

import (
	"math"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// recordedSwitcher is a switcher that can say which slot the roster last
// made active and when the roster was written: every tycswap switch writes
// both. Without it the engine cannot tell a switch from a login elsewhere.
type recordedSwitcher interface {
	RecordedSwitch() (slot string, at float64)
}

// LiveLoginChangedEvent reports a live login that changed without a tycswap
// switch (a /login in a session, another tool), once per change.
type LiveLoginChangedEvent struct {
	Ts       string
	From, To string
}

func (LiveLoginChangedEvent) Kind() string { return "live-login-changed" }

func (e LiveLoginChangedEvent) JSON() map[string]any {
	return baseJSON(e.Kind(), e.Ts, map[string]any{"from": e.From, "to": e.To})
}

func (e LiveLoginChangedEvent) Human() string {
	return "live login changed from account " + e.From + " to " + e.To + " without a tycswap switch"
}

// noteLiveLogin records, each real tick, the live slot it saw and when
// (lastSeenLive, lastSeenAt: the dashboard's last tick) and reports a change
// no tycswap switch since the previous tick explains. A failed write is
// logged; the tick goes on.
func (e *Engine) noteLiveLogin(cur string) {
	rs, ok := e.sw.(recordedSwitcher)
	if !ok || e.dryRun {
		return
	}
	var from string
	_, err := e.mutateState(func(st map[string]any) {
		seen, _ := st["lastSeenLive"].(string)
		since, _ := numOfAny(st["lastSeenAt"])
		if seen != "" && seen != cur && !switchedByTycswap(st, rs, cur, since) {
			from = seen
		}
		st["lastSeenLive"], st["lastSeenAt"] = cur, e.nowSeconds()
	})
	if err != nil {
		if e.log != nil {
			e.log.Warningf("Live login not recorded: %v; it is checked again next tick", err)
		}
		return
	}
	if from != "" {
		e.emit(LiveLoginChangedEvent{Ts: e.nowISO(), From: from, To: cur})
		if e.log != nil {
			e.log.Infof("Live login changed from account %s to %s without a tycswap switch", from, cur)
		}
	}
}

// switchedByTycswap reports whether a tycswap switch onto cur happened since
// the live slot was last seen: the engine's or start's own stamp, or the
// roster's record of any switch.
func switchedByTycswap(state map[string]any, rs recordedSwitcher, cur string, since float64) bool {
	if to, _ := state["lastSwitchTo"].(string); to == cur {
		if at, ok := numOfAny(state["lastSwitchAt"]); ok && at >= since {
			return true
		}
	}
	slot, at := rs.RecordedSwitch()
	return slot == cur && at >= math.Floor(since)
}

// blockedWithRoomKey is the state-file key holding the account set the last
// blocked-with-room report named.
const blockedWithRoomKey = "lastBlockedWithRoom"

// noteBlockedWithRoom reports a block the 5h and 7d bars do not explain, once
// per set of accounts it names (keptOut). The set is remembered in the state
// file and a tick without such a block forgets it, so a hysteresis hold, a
// cooldown or the same block on the next tick says nothing. A failed write is
// logged and tried again next tick; a dry run shows the event and keeps
// nothing.
func (e *Engine) noteBlockedWithRoom(cur, trigger string, axis usage.Class, blocked bool, quarantined, held map[string]bool, usageMap map[string]any, s settings.AutoSwitchSettings) {
	var room []string
	if blocked && (trigger == "at-limit" || trigger == "proactive") {
		room = e.keptOut(cur, trigger, axis, quarantined, held, usageMap, s)
	}
	key := strings.Join(room, ",")
	if e.dryRun {
		if key != "" {
			e.emit(BlockedWithRoomEvent{Ts: e.nowISO(), Accounts: room})
		}
		return
	}
	changed, err := e.rememberSet(blockedWithRoomKey, key)
	if err != nil {
		if e.log != nil {
			e.log.Warningf("Blocked-with-room not recorded: %v; it is checked again next tick", err)
		}
		return
	}
	if !changed || key == "" {
		return
	}
	e.emit(BlockedWithRoomEvent{Ts: e.nowISO(), Accounts: room})
	if e.log != nil {
		e.log.Warningf("Blocked on Account-%s although %s are below their 5h and 7d bars", cur, strings.Join(room, ", "))
	}
}

// rememberSet keeps value, the set a report names, under key in the state
// file ("" removes it) and reports a change: one report per set.
func (e *Engine) rememberSet(key, value string) (bool, error) {
	if last, _ := e.readState()[key].(string); last == value {
		return false, nil
	}
	changed := false
	_, err := e.mutateState(func(st map[string]any) {
		last, _ := st[key].(string)
		changed = last != value
		if value == "" {
			delete(st, key)
		} else {
			st[key] = value
		}
	})
	return changed, err
}

// RememberSet is rememberSet on the state file at statePath, for a report
// made outside the engine (DESIGN A112).
func RememberSet(statePath, key, value string) (bool, error) {
	e := &Engine{statePath: statePath, lockPath: stateLockPath(statePath)}
	return e.rememberSet(key, value)
}

// keptOut names, after a tick the live account's bars made move (at-limit,
// proactive), the enabled, eligible accounts below their 5h and 7d bars that
// it did not take for a reason those bars do not give: a counted model window
// (not reported, at 100 %, or over its bar when that window made the tick
// move) or an ownership hold met while preparing the target. One the
// hysteresis margin kept out is not named.
func (e *Engine) keptOut(cur, trigger string, axis usage.Class, quarantined, held map[string]bool, usageMap map[string]any, s settings.AutoSwitchSettings) []string {
	required := requiredModels(e.models)
	var out []string
	for _, num := range e.sw.SwitchableAccountNumbers() {
		if num == cur || quarantined[num] || e.sw.AccountKindFor(num) == "api_key" || usageDict(usageMap[num]) == nil {
			continue
		}
		h := e.headroomByClass(usageMap[num])
		_, weekOver := utilizationOver(h.Week, s.SevenDayThreshold)
		_, sessionOver := utilizationOver(h.Session, s.FiveHourThreshold)
		if h.Week == nil || weekOver || sessionOver {
			continue
		}
		modelUtil, modelOver := utilizationOver(h.Model, s.ModelThreshold)
		byModel := !hasRequiredModels(usageMap[num], required) || h.Model != nil && modelUtil >= 100 ||
			trigger == "proactive" && axis == usage.ClassModel && modelOver
		if byModel || held[num] {
			out = append(out, num)
		}
	}
	return sortNumeric(out)
}

// ReadSwitchStamp is the switch stamp of the state file at statePath, the
// slot a tycswap switch last made live and when (lastSwitchTo, lastSwitchAt),
// "" and 0 without one; lock-free like ReadLastTick.
func ReadSwitchStamp(statePath string) (slot string, at float64) {
	state := readStateFile(statePath)
	slot, _ = state["lastSwitchTo"].(string)
	at, _ = numOfAny(state["lastSwitchAt"])
	return slot, at
}

// RenumberLive carries a renumbering of accounts (old number to new) into the
// state file at statePath, under its lock: lastSeenLive and lastSwitchTo
// follow the account they name, every other field is kept. A move, swap or
// add --slot changes an account's number, not the login a store holds, and
// writes no switch stamp the Fable tick trusts (DESIGN A102). A file that
// names no renumbered account is not written, nor its lock taken.
func RenumberLive(statePath string, renumbered map[string]string) error {
	keys := []string{"lastSeenLive", "lastSwitchTo"}
	names := func(state map[string]any) bool {
		for _, k := range keys {
			if v, ok := state[k].(string); ok && renumbered[v] != "" {
				return true
			}
		}
		return false
	}
	if !names(readStateFile(statePath)) {
		return nil
	}
	return filelock.New(stateLockPath(statePath), 0).With(func() error {
		state := readStateFile(statePath)
		if !names(state) {
			return nil
		}
		for _, k := range keys {
			if v, ok := state[k].(string); ok && renumbered[v] != "" {
				state[k] = renumbered[v]
			}
		}
		return atomicfile.WriteJSON(statePath, state, atomicfile.Opts{})
	})
}

// ReadLastTick is when a real tick last saw the live login (lastSeenAt), as
// epoch seconds, or nil before one did; lock-free like ReadQuarantineEntries.
func ReadLastTick(statePath string) *float64 {
	at, ok := numOfAny(readStateFile(statePath)["lastSeenAt"])
	if !ok {
		return nil
	}
	return &at
}
