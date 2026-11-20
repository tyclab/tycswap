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

	"github.com/tyclab/tycswap/internal/core"
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

// storeTree is every path under the home and the store with its mode and
// content, to show a run wrote nothing.
func storeTree(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	var lines []string
	for _, root := range []string{home, paths.GetBackupRoot()} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				lines = append(lines, path+" "+err.Error())
				return nil
			}
			info, _ := d.Info()
			raw := []byte{}
			if d.Type().IsRegular() {
				raw, _ = os.ReadFile(path)
			}
			lines = append(lines, fmt.Sprintf("%s %v %d %x", path, info.Mode(), info.ModTime().UnixNano(), raw))
			return nil
		})
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
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
