// tycswap start --started, the SessionStart hook that ends a session's start
// hold (DESIGN A105).
package cli

import (
	"encoding/json"
	"io"
	"os"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
)

// startStarted releases the start hold startHoldEnv names and, once no other
// session is starting, runs one tick of both stores as `auto --once` does.
// It prints nothing, since a SessionStart hook's stdout goes into the
// session, and its problems go to the log; the caller exits 0 whatever
// happens.
func startStarted(in io.Reader) {
	token := os.Getenv(startHoldEnv)
	if token == "" || !sessionStarted(in) {
		return
	}
	if startStoreProblem(paths.GetBackupRoot()) != "" {
		return
	}
	sw, err := constructSwitcher(false, io.Discard)
	if err != nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			sw.Log.Warningf("start --started: %v", r)
		}
	}()
	defer oauth.RedirectOutput(io.Discard)()
	found, err := autoswitch.ReleaseStartHold(sw.StatePath(), sw.Clk, token)
	if err != nil {
		sw.Log.Warningf("start --started: the start hold was not released: %v", err)
	}
	if !found {
		return
	}
	autoswitch.WaitStartHolds(sw.StatePath(), sw.Clk)
	logProblems := func(ev autoswitch.Event) {
		if ev.Kind() == "error" {
			sw.Log.Warningf("start --started: %s", ev.Human())
		}
	}
	merged := settings.Load(sw.BackupDir())
	policy := defaultSessionPolicy(sw.Store, merged, sw.Clk.Now())
	onceTicks(sw, defaultEngine(sw, policy, logProblems, false), merged, logProblems, false)
}

// sessionStarted reports whether the SessionStart hook's input starts a
// session, as every source but clear and compact does. A terminal is not
// read; an unreadable or empty input counts as a start.
func sessionStarted(in io.Reader) bool {
	if f, ok := in.(*os.File); ok && isTTY(f) {
		return true
	}
	raw, _ := io.ReadAll(io.LimitReader(in, 1<<20))
	var hook struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(raw, &hook)
	return hook.Source != "clear" && hook.Source != "compact"
}
