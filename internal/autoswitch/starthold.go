package autoswitch

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/filelock"
)

// StartHoldS bounds a start hold: how long the login stays where a session
// started when that session never reports. An interactive session reached its
// SessionStart hook 3.9 s after exec on a 16-core box under load (print mode:
// 2.1 s plus Claude Code's own 3 s stdin wait), and its first request waited
// for the hook; 30 s is about eight times that (DESIGN A105).
const StartHoldS = 30.0

const (
	startHoldsKey = "startHolds"
	startHoldPoll = 100 * time.Millisecond
)

// liveStartHold reports whether a hold ending at end still runs at now. A
// hold ends at most StartHoldS after it was written, so one ending later came
// from another clock and holds nothing.
func liveStartHold(end any, now float64) bool {
	at, ok := numOfAny(end)
	return ok && at > now && at <= now+StartHoldS
}

// startHoldLeft is how long the last running start hold in state still runs,
// 0 without one.
func startHoldLeft(state map[string]any, now float64) float64 {
	holds, _ := state[startHoldsKey].(map[string]any)
	left := 0.0
	for _, end := range holds {
		if at, _ := numOfAny(end); liveStartHold(end, now) {
			left = max(left, at-now)
		}
	}
	return left
}

// settleStartHolds drops the holds in state that no longer run and, with
// hold, adds one ending StartHoldS from now; it returns the new hold's token.
func settleStartHolds(state map[string]any, now float64, hold bool) string {
	holds, _ := state[startHoldsKey].(map[string]any)
	kept := map[string]any{}
	for token, end := range holds {
		if liveStartHold(end, now) {
			kept[token] = end
		}
	}
	token := ""
	if hold {
		raw := make([]byte, 16)
		_, _ = rand.Read(raw)
		token = hex.EncodeToString(raw)
		kept[token] = now + StartHoldS
	}
	if len(kept) == 0 {
		delete(state, startHoldsKey)
	} else {
		state[startHoldsKey] = kept
	}
	return token
}

// ReleaseStartHold removes token's start hold from the state file at
// statePath under its lock, with every hold that no longer runs; found is
// false when token held nothing. A state without holds is neither locked nor
// written.
func ReleaseStartHold(statePath string, clk clock.Clock, token string) (found bool, err error) {
	if _, ok := readStateFile(statePath)[startHoldsKey].(map[string]any); !ok {
		return false, nil
	}
	err = filelock.New(stateLockPath(statePath), 0).With(func() error {
		state := readStateFile(statePath)
		holds, _ := state[startHoldsKey].(map[string]any)
		now := clock.Seconds(clk)
		found = liveStartHold(holds[token], now)
		before := len(holds)
		delete(holds, token)
		settleStartHolds(state, now, false)
		if after, _ := state[startHoldsKey].(map[string]any); len(after) == before {
			return nil
		}
		return atomicfile.WriteJSON(statePath, state, atomicfile.Opts{})
	})
	return found, err
}

// WaitStartHolds returns once the state file at statePath holds no running
// start hold, reading it every startHoldPoll on clk, and at the latest
// StartHoldS from now, when every hold it found has ended.
func WaitStartHolds(statePath string, clk clock.Clock) {
	sleep := time.Sleep
	if s, ok := clk.(clock.Sleeper); ok {
		sleep = s.Sleep
	}
	deadline := clock.Seconds(clk) + StartHoldS
	for now := clock.Seconds(clk); now < deadline && startHoldLeft(readStateFile(statePath), now) > 0; now = clock.Seconds(clk) {
		sleep(startHoldPoll)
	}
}
