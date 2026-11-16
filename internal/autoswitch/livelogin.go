package autoswitch

import "math"

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

// noteLiveLogin remembers the live slot a real tick saw (lastSeenLive,
// lastSeenAt) and reports a change no tycswap switch since lastSeenAt
// explains. A failed write is logged; the tick goes on.
func (e *Engine) noteLiveLogin(state map[string]any, cur string) {
	rs, ok := e.sw.(recordedSwitcher)
	if !ok || e.dryRun {
		return
	}
	if seen, _ := state["lastSeenLive"].(string); seen == cur {
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
