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
// hold, adds one ending StartHoldS from now under token, a new one for "";
// it returns the new hold's token.
func settleStartHolds(state map[string]any, now float64, hold bool, token string) string {
	holds, _ := state[startHoldsKey].(map[string]any)
	kept := map[string]any{}
	for token, end := range holds {
		if liveStartHold(end, now) {
			kept[token] = end
		}
	}
	if !hold {
		token = ""
	} else if token == "" {
		raw := make([]byte, 16)
		_, _ = rand.Read(raw)
		token = hex.EncodeToString(raw)
	}
	if token != "" {
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
// statePath under its lock, with every hold that no longer runs; a token that
// is not there removes nothing of its own. A state without holds is neither
// locked nor written, nor is one that loses none.
func ReleaseStartHold(statePath string, clk clock.Clock, token string) error {
	if _, ok := readStateFile(statePath)[startHoldsKey].(map[string]any); !ok {
		return nil
	}
	return filelock.New(stateLockPath(statePath), 0).With(func() error {
		state := readStateFile(statePath)
		holds, _ := state[startHoldsKey].(map[string]any)
		before := len(holds)
		delete(holds, token)
		settleStartHolds(state, clock.Seconds(clk), false, "")
		if after, _ := state[startHoldsKey].(map[string]any); len(after) == before {
			return nil
		}
		return atomicfile.WriteJSON(statePath, state, atomicfile.Opts{})
	})
}

// WaitStartHolds returns once none of the state files at statePaths holds a
// running start hold, reading them every startHoldPoll on clk, and at the
// latest StartHoldS from now, when every hold it found has ended.
func WaitStartHolds(statePaths []string, clk clock.Clock) {
	sleep := time.Sleep
	if s, ok := clk.(clock.Sleeper); ok {
		sleep = s.Sleep
	}
	held := func(now float64) bool {
		for _, path := range statePaths {
			if startHoldLeft(readStateFile(path), now) > 0 {
				return true
			}
		}
		return false
	}
	deadline := clock.Seconds(clk) + StartHoldS
	for now := clock.Seconds(clk); now < deadline && held(now); now = clock.Seconds(clk) {
		sleep(startHoldPoll)
	}
}
