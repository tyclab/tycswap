// Tests for a Fable session on the default login: the default tick does not
// follow it while the Fable store is usable, and start --check names it
// (DESIGN A108).
package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/testutil"
)

// fableOnDefault: the default login on 1 with 5h and 7d room and its Fable
// week spent, the Fable store on 2, 3 with Fable room (the shape reported on
// 2026-11-20).
var fableOnDefault = fableTickFixture{def: "1", live: "2", logins: []string{"2", "3"},
	usage: map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 50}, "3": {10, 20, 10}, "4": {10, 20, 100}}}

// liveFableSessionOnDefault records a live default-store session (this
// process) as Fable with no store, as its status line on the default login does.
func liveFableSessionOnDefault(t *testing.T, sw *core.Switcher) {
	t.Helper()
	pid := strconv.Itoa(os.Getpid())
	writeFile(t, filepath.Join(sw.DefaultProfileDir(), "sessions", pid+".json"), `{"pid":`+pid+`,"sessionId":"s-fable"}`)
	if _, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), "s-fable", "claude-fable-5-1", "", sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
}

// While the Fable store is usable the default tick counts 5h and 7d only and
// keeps the default login where it is; with the store off it follows the
// session and leaves its spent Fable week, as in 0.17.0.
func TestDefaultTickIgnoresAFableSessionOnTheDefaultLogin(t *testing.T) {
	t.Run("store-usable", func(t *testing.T) {
		sw, _ := fableOnDefault.build(t)
		liveFableSessionOnDefault(t, sw)
		_, out, _ := runCodex(t, "", "auto", "--once", "--json")
		def, _, _ := splitByStore(t, out)
		poll, ns := eventOf(def, "poll"), eventOf(def, "no-switch")
		windows, _ := poll["windowsPct"].(map[string]any)
		headroom, _ := poll["headroomPct"].(map[string]any)
		if eventOf(def, "switch") != nil || ns == nil || ns["reason"] != "below-threshold" || liveSlot(sw) != "1" {
			t.Fatalf("the default tick moved the default login (live %s):\n%s", liveSlot(sw), out)
		}
		if !reflect.DeepEqual(windows["1"], map[string]any{"5h": 10.0, "7d": 20.0}) || headroom["1"] != 80.0 {
			t.Fatalf("default poll counts %v (headroom %v), want 5h + 7d only:\n%s", windows["1"], headroom["1"], out)
		}
	})
	t.Run("store-off", func(t *testing.T) {
		fx := fableOnDefault
		fx.live = ""
		sw, _ := fx.build(t)
		liveFableSessionOnDefault(t, sw)
		_, out, _ := runCodex(t, "", "auto", "--once", "--json")
		def, _, _ := splitByStore(t, out)
		windows, _ := eventOf(def, "poll")["windowsPct"].(map[string]any)
		counted, _ := windows["1"].(map[string]any)
		moved := eventOf(def, "switch")
		if counted["Fable"] != 100.0 || moved == nil || moved["trigger"] != "at-limit" || liveSlot(sw) == "1" {
			t.Fatalf("store off: the Fable session is not followed (live %s):\n%s", liveSlot(sw), out)
		}
	})
}

const checkSession = "3d2dc434-6c1e-4a52-9f0b-1c2d3e4f5a6b"

func statusLine(session, model string) string {
	raw, _ := json.Marshal(map[string]any{"session_id": session, "model": map[string]any{"id": model, "display_name": "x"}, "cwd": "/tmp"})
	return string(raw)
}

// storeTree is every path under the home and the store with its mode, mtime
// and content, to show a run wrote nothing: a directory made and removed
// shows in its parent's mtime. Each path is read with Lstat, since a Windows
// listing reports a directory's time from its parent's index, which lags.
func storeTree(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	var lines []string
	for _, root := range []string{home, paths.GetBackupRoot()} {
		_ = filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
			info, statErr := os.Lstat(path)
			if err != nil || statErr != nil {
				lines = append(lines, fmt.Sprintf("%s %v %v", path, err, statErr))
				return nil
			}
			raw := []byte{}
			if info.Mode().IsRegular() {
				raw, _ = os.ReadFile(path)
			}
			lines = append(lines, fmt.Sprintf("%s %v %d %x", path, info.Mode(), info.ModTime().UnixNano(), raw))
			return nil
		})
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// A Claude Code lock (a directory made, then removed) and a temporary file
// made and removed in the store between two snapshots show in storeTree.
func TestStoreTreeSeesALockTakenAndReleased(t *testing.T) {
	fableStart(t, "on")
	before := storeTree(t)
	time.Sleep(20 * time.Millisecond)
	lock, err := cclock.AcquireProfileCredentials(paths.FableStoreDir(paths.GetBackupRoot()), 0, nil)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	lock.Release()
	f, err := os.CreateTemp(paths.FableScopeDir(paths.GetBackupRoot()), ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	if storeTree(t) == before {
		t.Fatal("storeTree saw no change after a lock directory and a temporary file were made and removed in the store")
	}
}

// start --check prints one line for a session on the store its model does
// not belong on, nothing otherwise, exits 0 and writes nothing (DESIGN A108).
func TestStartCheckNamesASessionOnTheWrongStore(t *testing.T) {
	onDefault := "⚠ Fable session on the default login: /exit, then claude --resume " + checkSession + " --model fable puts it on the Fable store\n"
	onFable := func(name, family string) string {
		return "⚠ " + name + " session on the Fable store: /exit, then claude --resume " + checkSession + " --model " + family + " puts it on the default login\n"
	}
	for _, tc := range []struct {
		name, state, stdin string
		fableStore         bool
		want               string
	}{
		{"fable on the default login", "on", statusLine(checkSession, "claude-fable-5-1"), false, onDefault},
		{"fable alias with 1m", "on", statusLine(checkSession, "fable[1m]"), false, onDefault},
		{"fable on an unknown live slot", "unknown", statusLine(checkSession, "claude-fable-5-1"), false, onDefault},
		{"fable without a uuid", "on", statusLine("s-1", "claude-fable-5-1"), false, "⚠ Fable session on the default login: restart it with --model fable\n"},
		{"fable without a session id", "on", `{"model":{"id":"claude-fable-5-1"}}`, false, "⚠ Fable session on the default login: restart it with --model fable\n"},
		{"session id not echoed", "on", statusLine("$(rm -rf ~); "+checkSession, "claude-fable-5-1"), false, "⚠ Fable session on the default login: restart it with --model fable\n"},
		{"opus on the Fable store", "on", statusLine(checkSession, "claude-opus-5-5"), true, onFable("Opus", "opus")},
		{"mythos on the Fable store", "on", statusLine(checkSession, "claude-mythos-5-2"), true, onFable("Mythos", "mythos")},
		{"sonnet on the Fable store", "on", statusLine(checkSession, "sonnet"), true, onFable("Sonnet", "sonnet")},
		{"haiku on the Fable store", "on", statusLine(checkSession, "claude-haiku-5-1"), true, onFable("Haiku", "haiku")},
		{"opus without a uuid on the Fable store", "on", statusLine("", "opus"), true, "⚠ Opus session on the Fable store: restart it with --model opus\n"},
		{"fable on the Fable store", "on", statusLine(checkSession, "claude-fable-5-1"), true, ""},
		{"opus on the default login", "on", statusLine(checkSession, "claude-opus-5-5"), false, ""},
		{"store off", "off", statusLine(checkSession, "claude-fable-5-1"), false, ""},
		{"store unusable", "unusable", statusLine(checkSession, "claude-fable-5-1"), false, ""},
		{"no Fable login", "none", statusLine(checkSession, "claude-fable-5-1"), false, ""},
		{"no Fable login, opus under the store's variable", "none", statusLine(checkSession, "claude-opus-5-5"), true, ""},
		{"provider id", "on", statusLine(checkSession, "us.anthropic.claude-fable-5-2-v1:0"), false, ""},
		{"unknown model", "on", statusLine(checkSession, "gpt-5"), false, ""},
		{"empty stdin", "on", "", false, ""},
		{"malformed stdin", "on", `{"session_id":`, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fableStart(t, tc.state)
			if tc.fableStore {
				testutil.Setenv(t, secureStorageVar, paths.FableStoreDir(paths.GetBackupRoot()))
			} else {
				testutil.Unsetenv(t, secureStorageVar)
			}
			before := storeTree(t)
			code, out, errb := runCodex(t, tc.stdin, "start", "--check")
			if code != 0 || out != tc.want || errb != "" {
				t.Fatalf("code=%d out=%q err=%q; want 0, %q", code, out, errb, tc.want)
			}
			if after := storeTree(t); after != before {
				t.Fatalf("start --check changed the home or the store:\nbefore\n%s\nafter\n%s", before, after)
			}
		})
	}
}

// start --check and start agree on where a Fable session goes: while start
// sends Fable sessions to the default login because no start login has a
// usable Fable login, the check names that cause, not a resume that lands on
// the default login again (DESIGN A105, A108).
func TestStartCheckAgreesWithStartOnAFableSession(t *testing.T) {
	cause := "⚠ Fable session on the default login: no start login has a Fable login; mark one: tycswap start --allow ID\n"
	for _, tc := range []struct {
		name           string
		logins, marked []string
		dead           string
		off, toDefault bool
	}{
		{"marked login without a Fable login", []string{"2"}, []string{"1"}, "", false, true},
		{"marked login's Fable login quarantined", []string{"1", "2"}, []string{"1"}, "1", false, true},
		{"marked login with a Fable login", []string{"1", "2"}, []string{"1"}, "", false, false},
		{"no marks", []string{"2"}, nil, "", false, false},
		{"marks with auto-switch off", []string{"2"}, []string{"1"}, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := fableTickFixture{def: "3", live: "2", logins: tc.logins, off: tc.off,
				usage: map[string][3]float64{"1": {10, 20, 10}, "2": {10, 20, 10}, "3": {10, 20, 30}, "4": {10, 20, 30}}}
			sw, rec := fableStartLogin(t, fx, tc.marked...)
			if tc.dead != "" {
				if err := autoswitch.Quarantine(fableStatePath(sw), sw.Clk, tc.dead, fableEmail(tc.dead), oauth.ErrInvalidGrant, fableLoginOf(tc.dead, farExpiry), func(autoswitch.Event) {}); err != nil {
					t.Fatal(err)
				}
			}
			testutil.Unsetenv(t, secureStorageVar)
			before := storeTree(t)
			code, out, errb := runCodex(t, statusLine(checkSession, "claude-fable-5-1"), "start", "--check")
			if code != 0 || errb != "" || storeTree(t) != before {
				t.Fatalf("start --check: code=%d stderr=%q, store unchanged %v", code, errb, storeTree(t) == before)
			}
			if tc.toDefault && out != cause || !tc.toDefault && !strings.Contains(out, "puts it on the Fable store") {
				t.Fatalf("start --check printed %q; start sends the session to the default login: %v", out, tc.toDefault)
			}
			code, _, stderr := runCLI(t, []string{"start", "--", "command", "--resume", checkSession, "--model", "fable"}, false, false)
			if onFable := envValue(rec.env, secureStorageVar) == fableStoreDir(); code != 23 || onFable == tc.toDefault {
				t.Fatalf("start: code=%d stderr=%q, child on the Fable store %v", code, stderr, onFable)
			}
		})
	}
}

func TestStartCheckIsAManagementForm(t *testing.T) {
	calls := startTestLaunch(t)
	for _, args := range [][]string{{"--check", "--list"}, {"--check", "command"}, {"--account", "1", "--check"}, {"--check", "--", "command"}, {"--started", "--check"}, {"--check", "--allow", "1"}} {
		code, _, errb := runCLI(t, append([]string{"start"}, args...), false, false)
		if code != 2 || *calls != 0 || !strings.Contains(errb, "management options") && !strings.Contains(errb, "--account cannot be combined") {
			t.Fatalf("args=%q code=%d calls=%d stderr=%q", args, code, *calls, errb)
		}
	}
}

func TestStartHelpNamesCheck(t *testing.T) {
	cleanHome(t)
	code, out, _ := runCLI(t, []string{"start", "--help"}, false, false)
	if code != 0 || !strings.Contains(out, "\n       tycswap start --check\n") {
		t.Fatalf("help lacks start --check:\n%s", out)
	}
}
