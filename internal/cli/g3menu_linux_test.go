package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

const fixtureMythosModel = "claude-mythos-5-2"

func menuStartFixture(t *testing.T) (*core.Switcher, *startRecord, string) {
	t.Helper()
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	setRecord(t, sw, "3", "mythos", true)
	dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("3"), "")
	writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
	startResolve = func(name string) (string, error) { return filepath.Join("/fixture", name), nil }
	return sw, rec, dir
}

func expectStartCode(t *testing.T, want int, args ...string) string {
	t.Helper()
	code, _, msg := runCLI(t, append([]string{"start"}, args...), false, false)
	if code != want {
		t.Fatalf("start %v: exit %d, want %d: %s", args, code, want, msg)
	}
	return msg
}

func TestMenuNativeMythosStart(t *testing.T) {
	sw, rec, dir := menuStartFixture(t)
	before := defaultLive(t, sw)
	for _, account := range [][]string{nil, {"--account", "3"}} {
		expectStartCode(t, 23, append([]string{"--mythos"}, account...)...)
		if !slices.Equal(rec.args[1:], []string{"--model", fixtureMythosModel}) {
			t.Fatalf("native model arguments = %q", rec.args)
		}
		assertNativeStore(t, rec.env, dir)
	}
	var out bytes.Buffer
	menuScript(t, "Start Mythos session", "claude #3")
	code := humanMenu("tycswap", "", ioStreams{out: &out, err: &out})
	if code != 23 || rec.execs != 3 || defaultLive(t, sw) != before {
		t.Fatalf("menu native result %d, launches %d: %s", code, rec.execs, &out)
	}
	assertNativeStore(t, rec.env, dir)
	for _, args := range [][]string{{"--", "claude", "--resume", "id"}, {"--list"}, {"--mythos"}} {
		expectStartCode(t, 2, append([]string{"--mythos"}, args...)...)
	}
	expectStartCode(t, 1, "--mythos", "--account", "2")
	t.Run("project authentication override", func(t *testing.T) {
		t.Chdir(t.TempDir())
		writeFile(t, filepath.Join(".claude", "settings.local.json"), `{"env":{"ANTHROPIC_AUTH_TOKEN":"fixture-auth"}}`)
		expectStartCode(t, 1, "--mythos")
	})
	setRecord(t, sw, "3", "disabled", true)
	expectStartCode(t, 1, "--mythos")
	if rec.execs != 3 {
		t.Fatalf("launched ineligible account %d times", rec.execs-3)
	}
}

func TestMenuStartPreferenceOnlyBareOnce(t *testing.T) {
	_, rec, dir := menuStartFixture(t)
	expectStartCode(t, 0, "--next", "mythos")
	for _, argv := range [][]string{{"tool"}, {"claude", "-p", "text"}, {"claude", "--continue"}, {"claude", "--model", "opus"}} {
		expectStartCode(t, 23, append([]string{"--"}, argv...)...)
		if slices.Contains(rec.args, fixtureMythosModel) {
			t.Fatalf("%v consumed the bare start", argv)
		}
	}
	expectStartCode(t, 23, "--", "claude")
	if !slices.Equal(rec.args, []string{"claude", "--model", fixtureMythosModel}) {
		t.Fatalf("bare launch = %v", rec.args)
	}
	assertNativeStore(t, rec.env, dir)
	expectStartCode(t, 23, "--", "claude")
	if len(rec.args) != 1 {
		t.Fatalf("preference was reused: %v", rec.args)
	}
	expectStartCode(t, 0, "--next", "mythos")
	expectStartCode(t, 0, "--next", "clear")
	expectStartCode(t, 23)
	if len(rec.args) != 1 {
		t.Fatalf("cleared preference was used: %v", rec.args)
	}
}

func TestMenuStartPreferenceSurvivesPlacementRefusal(t *testing.T) {
	sw, rec, dir := menuStartFixture(t)
	expectStartCode(t, 0, "--next", "mythos")
	setRecord(t, sw, "3", "disabled", true)
	if msg := expectStartCode(t, 1, "--", "claude"); rec.execs != 0 || strings.Count(msg, "\n") != 1 ||
		!strings.Contains(strings.ToLower(msg), "next start is set to mythos") || !strings.Contains(msg, "no enabled, marked") || !strings.Contains(msg, "tycswap start --next clear") {
		t.Fatalf("queued placement refusal lacks the request, reason or recovery: %q", msg)
	}
	setRecord(t, sw, "3", "disabled", false)
	expectStartCode(t, 23, "--", "claude")
	assertNativeStore(t, rec.env, dir)
}

func TestMenuStartPreferenceConcurrent(t *testing.T) {
	if os.Getenv("TYCSWAP_FIXTURE_RACING_START") == "1" {
		startResolveDefault = func() (string, error) { return "/fixture/claude", nil }
		startExec = func(_ string, args, env []string) (int, error) {
			if slices.Contains(args, fixtureMythosModel) {
				fmt.Println("queued model")
			}
			return 0, nil
		}
		expectStartCode(t, 0)
		return
	}
	menuStartFixture(t)
	expectStartCode(t, 0, "--next", "mythos")
	var children []*exec.Cmd
	var outputs []*bytes.Buffer
	for range 2 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMenuStartPreferenceConcurrent$")
		cmd.Env = append(os.Environ(), "TYCSWAP_FIXTURE_RACING_START=1")
		buf := new(bytes.Buffer)
		cmd.Stdout, cmd.Stderr = buf, buf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
		outputs = append(outputs, buf)
	}
	uses := 0
	for i, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("start race: %v %s", err, outputs[i])
		}
		uses += strings.Count(outputs[i].String(), "queued model")
	}
	if uses != 1 {
		t.Fatalf("queued model used %d times", uses)
	}
}

func TestMenuMythosActionParity(t *testing.T) {
	fableMenuPlatform(t, true)
	catalog := menuScript(t)
	menuCommands(ioStreams{err: io.Discard})
	for _, command := range []string{"use mythos", "use fable", "use default", "switch --store mythos", "start --mythos", "start --next mythos", "start --next clear"} {
		if !slices.ContainsFunc(catalog["Choose a command"], func(s string) bool { return strings.HasPrefix(s, command+"   ") }) {
			t.Errorf("Browse commands and options is missing %q", command)
		}
	}
	for _, tc := range []struct{ cmd, flag string }{{"use mythos", "--session="}, {"use fable", "--session="}, {"use default", "--session="}, {"start --mythos", "--account="}, {"switch --store mythos", "--json"}} {
		list := menuScript(t, tc.cmd+"   ")
		menuCommands(ioStreams{err: io.Discard})
		if !slices.ContainsFunc(list["Options for "+tc.cmd], func(s string) bool { return strings.HasPrefix(s, tc.flag) }) {
			t.Errorf("%s is missing option %s", tc.cmd, tc.flag)
		}
	}
}

func TestMenuMythosAccountAndNextStart(t *testing.T) {
	sw, _, _ := menuStartFixture(t)
	setRecord(t, sw, "4", "mythos", true)
	otherDir := store.MythosLoginDir(sw.BackupDir(), fableEmail("4"), "")
	writeFile(t, filepath.Join(otherDir, ".credentials.json"), dryCreds("independent-b", "fixture-refresh-b"))
	cmds := workflowCommands(t)
	s := ioStreams{err: io.Discard, out: io.Discard}
	menuScript(t, "claude #4", "Use for Mythos")
	menuManage(s)
	if !reflect.DeepEqual(*cmds, [][]string{{"switch", "--store", "mythos", "4"}}) {
		t.Fatalf("account action %v", *cmds)
	}
	menuScript(t, "Set next start")
	humanMenu("tycswap", "", s)
	if !slices.Equal((*cmds)[len(*cmds)-1], []string{"start", "--next", "mythos"}) {
		t.Fatalf("queue action: %v", *cmds)
	}
	expectStartCode(t, 0, "--next", "mythos")
	menuScript(t, "Set next start")
	humanMenu("tycswap", "", s)
	if !slices.Equal((*cmds)[len(*cmds)-1], []string{"start", "--next", "clear"}) {
		t.Fatalf("clear action: %v", *cmds)
	}
}

func TestMenuSessionMovesInvokeExplicitUse(t *testing.T) {
	_, rec, _ := menuStartFixture(t)
	linkedTestStart(t, rec, placementSession, "opus")
	sessionRegistryEntry(t, placementSession, "")
	cmds := workflowCommands(t)
	s := ioStreams{err: io.Discard, out: io.Discard}
	for _, target := range []struct{ label, arg string }{{"Move to Mythos login", "mythos"}, {"Move to Fable store", "fable"}} {
		menuScript(t, "Sessions", placementSession, target.label)
		humanMenu("tycswap", "", s)
		if len(*cmds) == 0 || !slices.Equal((*cmds)[len(*cmds)-1], []string{"use", target.arg, "--session", placementSession}) {
			t.Fatalf("session action %q dispatched %v", target.label, *cmds)
		}
	}
}

func TestMenuNextStartFallsBackForUnreadablePreference(t *testing.T) {
	sw, rec, _ := menuStartFixture(t)
	path := filepath.Join(sw.BackupDir(), "next-start.json")
	for i, raw := range []string{`{}`, `{"model":null}`, `{"model":"other"}`, `[`} {
		writeFile(t, path, raw)
		msg := expectStartCode(t, 23, "--", "claude")
		if rec.execs != i+1 || !slices.Equal(rec.args, []string{"claude"}) || strings.Count(msg, "\n") != 1 || !strings.Contains(msg, path) || !strings.Contains(msg, "tycswap start --next clear") {
			t.Fatalf("invalid request %q: launches=%d args=%v notice=%q", raw, rec.execs, rec.args, msg)
		}
		if got := readOrAbsent(t, path); got != raw {
			t.Fatalf("unreadable preference rewritten to %q", got)
		}
	}
}

func TestExplicitNextStartReplacesUnreadablePreference(t *testing.T) {
	sw, _, _ := menuStartFixture(t)
	for _, choice := range []string{"clear", "mythos"} {
		t.Run(choice, func(t *testing.T) {
			path := filepath.Join(sw.BackupDir(), "next-start.json")
			writeFile(t, path, "[")
			expectStartCode(t, 0, "--next", choice)
			got, err := store.NextStartModel(sw.BackupDir())
			want := choice
			if choice == "clear" {
				want = ""
			}
			if err != nil || got != want {
				t.Fatalf("saved request after repair = %q, %v; want %q", got, err, want)
			}
		})
	}
}
func TestMenuShowsLoginInUseAndLinkedOwner(t *testing.T) {
	_, rec, _ := menuStartFixture(t)
	linkedTestStart(t, rec, placementSession, "opus")
	sessionRegistryEntry(t, placementSession, "")
	streams := ioStreams{err: io.Discard, out: io.Discard}
	offered := menuScript(t)
	menuMoveSession(streams, "")
	if !strings.Contains(strings.Join(offered["Move which session?"], " "), "default login · #1") {
		t.Errorf("session choices %v", offered)
	}
	pickAccountRows(streams, "Choose an account", []reporting.AccountSnapshot{
		{Number: "2", MythosLogin: &reporting.MythosLogin{Status: "ok", Live: true}},
		{Number: "3", MythosLogin: &reporting.MythosLogin{Status: "ok", Selectable: true}},
	})
	choices := offered["Choose an account"]
	if len(choices) != 2 || !strings.HasSuffix(choices[0], "Mythos login: in use") || !strings.HasSuffix(choices[1], "Mythos login") {
		t.Errorf("account choices %q", choices)
	}
}
