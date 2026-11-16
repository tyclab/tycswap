package autoswitch

import (
	"math"
	"strings"

	"github.com/tyclab/tycswap/internal/settings"
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

// reportBlockedWithRoom names, after a blocked tick, every enabled, eligible
// account below its 5h and 7d bars: a block those bars do not explain (a
// model window, an ownership hold) shows in the journal and the log.
func (e *Engine) reportBlockedWithRoom(cur string, quarantined map[string]bool, usageMap map[string]any, s settings.AutoSwitchSettings) {
	var room []string
	for _, num := range e.sw.SwitchableAccountNumbers() {
		if quarantined[num] || e.sw.AccountKindFor(num) == "api_key" || usageDict(usageMap[num]) == nil {
			continue
		}
		h := e.headroomByClass(usageMap[num])
		_, weekOver := utilizationOver(h.Week, s.SevenDayThreshold)
		_, sessionOver := utilizationOver(h.Session, s.FiveHourThreshold)
		if h.Week != nil && !weekOver && !sessionOver {
			room = append(room, num)
		}
	}
	if len(room) == 0 {
		return
	}
	room = sortNumeric(room)
	e.emit(BlockedWithRoomEvent{Ts: e.nowISO(), Accounts: room})
	if e.log != nil {
		e.log.Warningf("Blocked on Account-%s although %s are below their 5h and 7d bars", cur, strings.Join(room, ", "))
	}
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
