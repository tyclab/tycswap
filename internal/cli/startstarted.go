// tycswap start --started, the SessionStart hook that ends a session's start
// hold (DESIGN A105).
package cli

import (
	"cmp"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

// startStarted records the family startFamilyEnv names for the hook's
// session, so the policy knows a Mythos session before its first status line
// (DESIGN A106). In a linked session it binds the link and places the session
// as its status line would (A109, A120, A122); a session that came up on a
// Mythos login of another family's start leaves it also for a login at its
// limit, and one tick then moves that login on (A125). It then
// releases the start hold startHoldEnv names, if it is still there, and, once
// no other session is starting, runs one tick of both stores as `auto --once`
// does: also when the hold had ended, since a session that took longer than
// the bound to come up still needs the login moved. It prints nothing, since a
// SessionStart hook's stdout goes into the session, and its problems go to the
// log; the caller exits 0 whatever happens.
func startStarted(in io.Reader) {
	token, family := os.Getenv(startHoldEnv), os.Getenv(startFamilyEnv)
	if token == "" && family == "" && mythosSessionDir == "" && sessionLinkPath == "" {
		return
	}
	source, session, observedModel := sessionStarted(in)
	if source == "clear" || source == "compact" || startStoreProblem(paths.GetBackupRoot()) != "" {
		return
	}
	scope := ""
	if sessionLinkPath != "" || mythosSessionDir != "" || fableSessionProcess && validMythosDigest(os.Getenv(recoveredFableEnv)) {
		if sessionLinkPath == "" && !sessionUUID.MatchString(session) {
			return
		}
		scope = observedStore()
		if actual := reporting.ClaudeFamily(observedModel); actual != "" {
			family = actual
		}
	}
	if sessionLinkPath != "" {
		if sessionUUID.MatchString(session) {
			root := paths.GetBackupRoot()
			if err := reporting.BindSessionLink(root, session, sessionLinkPath, family, scope, time.Now()); err != nil {
				logging.New(root, false).Warningf("start --started: %v", err)
			} else if model := cmp.Or(observedModel, family); model != "" {
				// The session moves on from here, also one that draws no status
				// line: its client has read its login's settings. Claude Code may
				// name no model; the family is then the one start read. The move
				// waits for the client's token refresh (DESIGN A120, A122, A123).
				defer placeStartedSession(session, model, token)
			}
		}
	} else if info, err := os.Stat(paths.GetBackupRoot()); family != "" && err == nil && info.IsDir() {
		if err := reporting.RecordSessionStart(paths.GetBackupRoot(), session, family, scope, time.Now()); err != nil {
			logging.New(paths.GetBackupRoot(), false).Warningf("start --started: %v", err)
		}
	}
	if token == "" {
		return
	}
	startedTick(token)
}

// placeStartedSession is the hook's placement of its linked session. When the
// session left the login it came up on for one at its limit and no start hold
// has run the tick, one tick runs now (DESIGN A125).
func placeStartedSession(session, model, token string) {
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return
	}
	if result, err := placeSession(sw, session, sessionLinkPath, model, "", false, true, "hook"); err == nil && result.spent && token == "" {
		startedTick("")
	}
}

// startedTick releases the start hold token names ("" for none) and, once no
// other session is starting, runs one tick of both stores.
func startedTick(token string) {
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
	statePaths := releaseStartHolds(sw, token)
	autoswitch.WaitStartHolds(statePaths, sw.Clk)
	logProblems := func(ev autoswitch.Event) {
		if ev.Kind() == "error" {
			sw.Log.Warningf("start --started: %s", ev.Human())
		}
	}
	merged := settings.Load(sw.BackupDir())
	policy := defaultSessionPolicy(sw.Store, merged, sw.Clk.Now())
	onceTicks(sw, defaultEngine(sw, policy, logProblems, false), merged, logProblems, false)
}

func releaseStartHolds(sw *core.Switcher, token string) []string {
	// Older launchers could record the same token in both files.
	statePaths := []string{sw.StatePath()}
	if f, err := sw.Store.ForFable(); err == nil {
		statePaths = append(statePaths, f.StatePath())
	}
	if token == "" {
		return statePaths
	}
	for _, path := range statePaths {
		if err := autoswitch.ReleaseStartHold(path, sw.Clk, token); err != nil {
			sw.Log.Warningf("start --started: the start hold was not released: %v", err)
		}
	}
	return statePaths
}

// A terminal is not read. An empty source still releases an existing hold.
func sessionStarted(in io.Reader) (string, string, string) {
	if f, ok := in.(*os.File); ok && isTTY(f) {
		return "", "", ""
	}
	raw, _ := io.ReadAll(io.LimitReader(in, 1<<20))
	var hook struct {
		Source    string `json:"source"`
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
	}
	_ = json.Unmarshal(raw, &hook)
	return hook.Source, hook.SessionID, hook.Model
}
