package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/wincred"
)

// fakePrompter answers every prompt with a fixed line, or reports "no input"
// the way a non-terminal does.
type fakePrompter struct {
	lifecycle.Prompter
	answer string
	ok     bool
	asked  []string
}

func (f *fakePrompter) Prompt(message string) (string, bool) {
	f.asked = append(f.asked, message)
	return f.answer, f.ok
}

func withPrompter(t *testing.T, p lifecycle.Prompter) {
	t.Helper()
	prev := lifecycle.ActivePrompter
	lifecycle.ActivePrompter = p
	t.Cleanup(func() { lifecycle.ActivePrompter = prev })
}

func withRunningSessions(t *testing.T, n int) {
	t.Helper()
	prev := runningSessions
	runningSessions = func() int { return n }
	t.Cleanup(func() { runningSessions = prev })
}

func withStdinTerminal(t *testing.T, tty bool) {
	t.Helper()
	prev := stdinIsTerminal
	stdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { stdinIsTerminal = prev })
}

// TestConfirmAuthModeChangeRefusesANonTerminal: an auth-mode change is never
// made because nobody objected. Stdin that is not a terminal (a pipe, even an
// open one that never writes) is refused at once, without reading, and
// without --yes (DESIGN A33). The notice still prints.
func TestConfirmAuthModeChangeRefusesANonTerminal(t *testing.T) {
	withRunningSessions(t, 0)
	withStdinTerminal(t, false)
	fp := &fakePrompter{answer: "y", ok: true}
	withPrompter(t, fp)
	var out bytes.Buffer
	if got := confirmAuthModeChange(&out, "Switch to API-key account #3?", "detail", false); got != authModeNotATerminal {
		t.Fatalf("result = %v, want not-a-terminal", got)
	}
	if len(fp.asked) != 0 {
		t.Errorf("read from a non-terminal: %v", fp.asked)
	}
	if !strings.Contains(out.String(), "restart") {
		t.Errorf("output = %q, want the restart notice", out.String())
	}
}

// TestConfirmAuthModeChangeEndOfInputDeclines: on a terminal, end of input
// (Ctrl-D) is no answer and declines.
func TestConfirmAuthModeChangeEndOfInputDeclines(t *testing.T) {
	withRunningSessions(t, 0)
	withStdinTerminal(t, true)
	withPrompter(t, &fakePrompter{ok: false})
	var out bytes.Buffer
	if got := confirmAuthModeChange(&out, "q?", "detail", false); got != authModeDeclined {
		t.Fatalf("result = %v, want declined", got)
	}
}

// TestConfirmAuthModeChangeAssumeYesSkipsThePrompt: --yes is the answer for
// scripts. The prompt is skipped, terminal or not, but the notice still
// prints, so a scripted run leaves the same trace as an interactive one.
func TestConfirmAuthModeChangeAssumeYesSkipsThePrompt(t *testing.T) {
	withRunningSessions(t, 0)
	for _, tty := range []bool{true, false} {
		withStdinTerminal(t, tty)
		fp := &fakePrompter{answer: "n", ok: true}
		withPrompter(t, fp)
		var out bytes.Buffer
		if got := confirmAuthModeChange(&out, "Switch to API-key account #3?", "detail", true); got != authModeApproved {
			t.Fatalf("terminal=%v: --yes must approve, got %v", tty, got)
		}
		if len(fp.asked) != 0 {
			t.Errorf("terminal=%v: prompted despite --yes: %v", tty, fp.asked)
		}
		if !strings.Contains(out.String(), "restart") {
			t.Errorf("output = %q, want the restart notice even with --yes", out.String())
		}
	}
}

func TestConfirmAuthModeChangeTakesYes(t *testing.T) {
	withRunningSessions(t, 0)
	withStdinTerminal(t, true)
	for _, tc := range []struct {
		answer string
		want   int
	}{{"y", authModeApproved}, {"Y", authModeApproved}, {" y ", authModeApproved},
		{"n", authModeDeclined}, {"", authModeDeclined}, {"yes please", authModeDeclined}} {
		fp := &fakePrompter{answer: tc.answer, ok: true}
		withPrompter(t, fp)
		var out bytes.Buffer
		if got := confirmAuthModeChange(&out, "q?", "detail", false); got != tc.want {
			t.Errorf("answer %q → %v, want %v", tc.answer, got, tc.want)
		}
	}
}

// TestRestartNoticeCountsTheSessions: the notice names how many sessions keep
// their login, because "restart Claude Code" reads as optional until you know
// it means eleven windows.
func TestRestartNoticeCountsTheSessions(t *testing.T) {
	for n, want := range map[int]string{
		0:  "Any Claude Code session that is already running keeps its current login until you restart it.",
		1:  "1 Claude Code session is running; it keeps its current login until you restart it.",
		11: "11 Claude Code sessions are running; they keep their current login until each one is restarted.",
	} {
		withRunningSessions(t, n)
		if got := restartNotice(); got != want {
			t.Errorf("%d sessions: %q, want %q", n, got, want)
		}
	}
}

// TestParseAcceptsYesForSwitch: the --yes the refusal names reaches the switch
// path, in both spellings and after the verb.
func TestParseAcceptsYesForSwitch(t *testing.T) {
	for _, argv := range [][]string{
		{"--switch-to", "2", "--yes"},
		{"--switch-to", "2", "-y"},
		translateSubcommand([]string{"switch", "2", "--yes"}),
		translateSubcommand([]string{"switch", "2", "-y"}),
	} {
		var errb bytes.Buffer
		res := parseArgs("tycswap", argv, &bytes.Buffer{}, &errb)
		if res.done {
			t.Fatalf("%v: parse failed with code %d (stderr=%q)", argv, res.code, errb.String())
		}
		if !res.p.yes {
			t.Errorf("%v did not set yes", argv)
		}
	}
}

// TestYesOnlyWithSwitchTo: --yes answers the API-key confirmation and nothing
// else, so anywhere else it is a usage error rather than silently ignored.
func TestYesOnlyWithSwitchTo(t *testing.T) {
	cleanHome(t)
	for _, argv := range [][]string{{"list", "--yes"}, {"switch", "--yes"}, {"remove", "2", "-y"}} {
		code, _, errStr := runCLI(t, argv, false, false)
		if code != 2 || !strings.Contains(errStr, "--yes can only be used with 'switch <num|email>'") {
			t.Errorf("%v: exit %d, stderr %q; want exit 2 naming the rule", argv, code, errStr)
		}
	}
}

// apiKeySwitcher is the Python fixture store (slot 3 is the API-key account
// key@example.com, slot 1 alice is live) with fakes for every seam.
func apiKeySwitcher(t *testing.T) *core.Switcher {
	t.Helper()
	sw := fixtureSwitcher(t)
	if sw.Store.AccountKindFor("3") != "api_key" {
		t.Fatalf("fixture slot 3 kind = %q, want api_key", sw.Store.AccountKindFor("3"))
	}
	return sw
}

// TestSwitchToAPIKeyAccountAsks: `switch <n>` onto an API-key account asks
// first, names the running sessions, and only an answer of y (or --yes)
// records the approval the switch layer needs; every refusal is an error
// (exit 1). Stdin that is not a terminal is refused without being read.
// --json never asks: --yes approves it, and without --yes the switch is
// refused by the switch layer.
func TestSwitchToAPIKeyAccountAsks(t *testing.T) {
	for _, tc := range []struct {
		name            string
		prompter        *fakePrompter
		notTerminal     bool
		jsonOut, yes    bool
		wantErr         string
		wantAsked       bool
		wantApproved    bool
		wantOut, notOut []string
	}{
		{name: "answered y", prompter: &fakePrompter{answer: "y", ok: true}, wantAsked: true, wantApproved: true,
			wantOut: []string{"billed per token", "2 Claude Code sessions are running"}},
		{name: "answered n", prompter: &fakePrompter{answer: "n", ok: true}, wantAsked: true,
			wantErr: "Cancelled: not switched to API-key account #3."},
		{name: "end of input", prompter: &fakePrompter{ok: false}, wantAsked: true,
			wantErr: "Cancelled: not switched to API-key account #3."},
		{name: "not a terminal", prompter: &fakePrompter{answer: "y", ok: true}, notTerminal: true,
			wantErr: "Not a terminal — rerun with --yes to confirm the switch to API-key account #3.",
			wantOut: []string{"2 Claude Code sessions are running"}},
		{name: "--yes", prompter: &fakePrompter{answer: "n", ok: true}, yes: true, wantApproved: true,
			wantOut: []string{"2 Claude Code sessions are running"}},
		{name: "--yes, not a terminal", prompter: &fakePrompter{answer: "n", ok: true}, notTerminal: true, yes: true, wantApproved: true},
		{name: "--json", prompter: &fakePrompter{answer: "y", ok: true}, jsonOut: true, notOut: []string{"billed"}},
		{name: "--json --yes", prompter: &fakePrompter{answer: "n", ok: true}, jsonOut: true, yes: true, wantApproved: true, notOut: []string{"billed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw := apiKeySwitcher(t)
			withRunningSessions(t, 2)
			withStdinTerminal(t, !tc.notTerminal)
			withPrompter(t, tc.prompter)
			var out bytes.Buffer
			err := confirmSwitchToAPIKey(&out, "3", sw, tc.jsonOut, tc.yes, false)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v, want none (output %q)", err, out.String())
			case tc.wantErr != "":
				var ce *cerr.Error
				if !errors.As(err, &ce) || ce.Kind != cerr.KindValidation || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want the ValidationError %q", err, tc.wantErr)
				}
			}
			if asked := len(tc.prompter.asked) > 0; asked != tc.wantAsked {
				t.Errorf("asked = %v (%v), want %v", asked, tc.prompter.asked, tc.wantAsked)
			}
			if tc.wantAsked && !strings.Contains(tc.prompter.asked[0], "Switch to API-key account #3?") {
				t.Errorf("question = %q", tc.prompter.asked[0])
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output %q lacks %q", out.String(), want)
				}
			}
			for _, not := range tc.notOut {
				if strings.Contains(out.String(), not) {
					t.Errorf("output %q has %q", out.String(), not)
				}
			}
			// The approval is what lets the switch layer through, once.
			_, err = sw.SwitchToForce("3", true, false)
			if (err == nil) != tc.wantApproved {
				t.Errorf("switch after the prompt: err = %v, want approved=%v", err, tc.wantApproved)
			}
		})
	}
}

// TestSwitchToASubscriptionAccountNeverAsks: every other switch is unchanged.
func TestSwitchToASubscriptionAccountNeverAsks(t *testing.T) {
	sw := apiKeySwitcher(t)
	withStdinTerminal(t, true)
	fp := &fakePrompter{answer: "n", ok: true}
	withPrompter(t, fp)
	var out bytes.Buffer
	if err := confirmSwitchToAPIKey(&out, "2", sw, false, false, false); err != nil || len(fp.asked) != 0 || out.Len() != 0 {
		t.Errorf("a subscription target was gated: err %v, asked %v, output %q", err, fp.asked, out.String())
	}
	if err := confirmSwitchToAPIKey(&out, "nobody@example.com", sw, false, false, false); err != nil || len(fp.asked) != 0 {
		t.Error("an unknown identifier must be left to the switch to report")
	}
}

// TestSwitchToTheActiveAPIKeyAccountDoesNotAsk: the API-key account already in
// use is not asked about; the switch reports it as already active. --force
// still asks, since it rewrites the live login.
func TestSwitchToTheActiveAPIKeyAccountDoesNotAsk(t *testing.T) {
	sw := apiKeySwitcher(t)
	withRunningSessions(t, 0)
	withStdinTerminal(t, true)
	withPrompter(t, &fakePrompter{answer: "y", ok: true})
	var out bytes.Buffer
	if err := confirmSwitchToAPIKey(&out, "3", sw, false, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.SwitchToForce("3", true, false); err != nil {
		t.Fatalf("switch onto #3: %v", err)
	}
	fp := &fakePrompter{answer: "n", ok: true}
	withPrompter(t, fp)
	if err := confirmSwitchToAPIKey(&out, "3", sw, false, false, false); err != nil || len(fp.asked) != 0 {
		t.Fatalf("the active API key was asked about: err %v, asked %v", err, fp.asked)
	}
	res, err := sw.SwitchToForce("3", true, false)
	if err != nil || res["reason"] != "already-active" {
		t.Errorf("switch onto the active API key = %v, %v, want already-active", res, err)
	}
	if err := confirmSwitchToAPIKey(&out, "3", sw, false, false, true); err == nil || len(fp.asked) != 1 {
		t.Errorf("--force onto the active API key: err %v, asked %v; want asked and declined", err, fp.asked)
	}
}

// TestSwitchJSONOntoAnAPIKeyEndToEnd drives the front controller: `switch 3
// --json` is refused with the approval error envelope (exit 1) and nothing is
// switched; `switch 3 --json --yes` switches.
func TestSwitchJSONOntoAnAPIKeyEndToEnd(t *testing.T) {
	apiKeySwitcher(t) // builds the fixture home in $HOME
	prev := newSwitcher
	newSwitcher = func(opts store.Options) (*core.Switcher, error) {
		opts.Keychain, opts.OAuth, opts.WinCred, opts.Stderr = homeKeychain(), &oauth.FakeClient{}, wincred.NewFake(), io.Discard
		return core.New(opts)
	}
	t.Cleanup(func() { newSwitcher = prev })
	geteuidPrev := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = geteuidPrev })
	withPrompter(t, &fakePrompter{answer: "y", ok: true})
	withStdinTerminal(t, false)

	// Not a terminal: refused at once, exit 1, nothing switched.
	code, _, errStr := runCLI(t, []string{"switch", "3"}, false, false)
	if code != 1 || !strings.Contains(errStr, "Not a terminal — rerun with --yes") {
		t.Fatalf("switch 3 without a terminal: exit %d, stderr %q", code, errStr)
	}

	code, out, _ := runCLI(t, []string{"switch", "3", "--json"}, false, false)
	if code != 1 {
		t.Fatalf("switch 3 --json: exit %d, want 1 (stdout %q)", code, out)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not one JSON document: %q", out)
	}
	if msg, _ := env["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "authenticates with an API key") {
		t.Errorf("error envelope = %v, want the approval refusal", env)
	}

	code, out, errStr = runCLI(t, []string{"switch", "3", "--json", "--yes"}, false, false)
	if code != 0 {
		t.Fatalf("switch 3 --json --yes: exit %d (stdout %q, stderr %q)", code, out, errStr)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout is not one JSON document: %q", out)
	}
	if res["switched"] != true {
		t.Errorf("result = %v, want switched", res)
	}
}
