package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
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

// TestConfirmAuthModeChangeNeedsAnAnswer: an auth-mode change is never made
// because nobody objected. Without a terminal to answer and without --yes it
// is refused (DESIGN A33), and the refusal names the flag that answers it.
func TestConfirmAuthModeChangeNeedsAnAnswer(t *testing.T) {
	withRunningSessions(t, 0)
	fp := &fakePrompter{ok: false} // a non-terminal: no input available
	withPrompter(t, fp)
	var out bytes.Buffer
	if confirmAuthModeChange(&out, "Switch to API-key account #3?", "detail", false) {
		t.Fatal("a non-terminal must not be taken as approval")
	}
	if !strings.Contains(out.String(), "rerun with --yes to confirm") {
		t.Errorf("output = %q, want it to name --yes", out.String())
	}
	if !strings.Contains(out.String(), "restart") {
		t.Errorf("output = %q, want the restart notice", out.String())
	}
}

// TestConfirmAuthModeChangeAssumeYesSkipsThePrompt: --yes is that answer. The
// prompt is skipped, but the notice still prints, so a scripted run leaves the
// same trace as an interactive one.
func TestConfirmAuthModeChangeAssumeYesSkipsThePrompt(t *testing.T) {
	withRunningSessions(t, 0)
	fp := &fakePrompter{answer: "n", ok: true}
	withPrompter(t, fp)
	var out bytes.Buffer
	if !confirmAuthModeChange(&out, "Switch to API-key account #3?", "detail", true) {
		t.Fatal("--yes must approve")
	}
	if len(fp.asked) != 0 {
		t.Errorf("prompted despite --yes: %v", fp.asked)
	}
	if !strings.Contains(out.String(), "restart") {
		t.Errorf("output = %q, want the restart notice even with --yes", out.String())
	}
}

func TestConfirmAuthModeChangeTakesYes(t *testing.T) {
	withRunningSessions(t, 0)
	for _, tc := range []struct {
		answer string
		want   bool
	}{{"y", true}, {"Y", true}, {" y ", true}, {"n", false}, {"", false}, {"yes please", false}} {
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
// records the approval the switch layer needs. --json never asks: --yes
// approves it, and without --yes the switch is refused.
func TestSwitchToAPIKeyAccountAsks(t *testing.T) {
	for _, tc := range []struct {
		name            string
		prompter        *fakePrompter
		jsonOut, yes    bool
		wantStop        bool
		wantAsked       bool
		wantApproved    bool
		wantOut, notOut []string
	}{
		{name: "answered y", prompter: &fakePrompter{answer: "y", ok: true}, wantAsked: true, wantApproved: true,
			wantOut: []string{"billed per token", "2 Claude Code sessions are running"}},
		{name: "answered n", prompter: &fakePrompter{answer: "n", ok: true}, wantAsked: true, wantStop: true,
			wantOut: []string{"Cancelled."}},
		{name: "no terminal", prompter: &fakePrompter{ok: false}, wantAsked: true, wantStop: true,
			wantOut: []string{"Not a terminal — rerun with --yes to confirm.", "Cancelled."}},
		{name: "--yes", prompter: &fakePrompter{answer: "n", ok: true}, yes: true, wantApproved: true,
			wantOut: []string{"2 Claude Code sessions are running"}},
		{name: "--json", prompter: &fakePrompter{answer: "y", ok: true}, jsonOut: true, notOut: []string{"billed"}},
		{name: "--json --yes", prompter: &fakePrompter{answer: "n", ok: true}, jsonOut: true, yes: true, wantApproved: true, notOut: []string{"billed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw := apiKeySwitcher(t)
			withRunningSessions(t, 2)
			withPrompter(t, tc.prompter)
			var out bytes.Buffer
			if stop := confirmSwitchToAPIKey(&out, "3", sw, tc.jsonOut, tc.yes); stop != tc.wantStop {
				t.Fatalf("stop = %v, want %v (output %q)", stop, tc.wantStop, out.String())
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
			_, err := sw.SwitchToForce("3", true, false)
			if (err == nil) != tc.wantApproved {
				t.Errorf("switch after the prompt: err = %v, want approved=%v", err, tc.wantApproved)
			}
		})
	}
}

// TestSwitchToASubscriptionAccountNeverAsks: every other switch is unchanged.
func TestSwitchToASubscriptionAccountNeverAsks(t *testing.T) {
	sw := apiKeySwitcher(t)
	fp := &fakePrompter{answer: "n", ok: true}
	withPrompter(t, fp)
	var out bytes.Buffer
	if confirmSwitchToAPIKey(&out, "2", sw, false, false) || len(fp.asked) != 0 || out.Len() != 0 {
		t.Errorf("a subscription target was gated: asked %v, output %q", fp.asked, out.String())
	}
	if confirmSwitchToAPIKey(&out, "nobody@example.com", sw, false, false) || len(fp.asked) != 0 {
		t.Error("an unknown identifier must be left to the switch to report")
	}
}

// TestSwitchJSONOntoAnAPIKeyEndToEnd drives the front controller: `switch 3
// --json` is refused with the approval error envelope (exit 1) and nothing is
// switched; `switch 3 --json --yes` switches.
func TestSwitchJSONOntoAnAPIKeyEndToEnd(t *testing.T) {
	apiKeySwitcher(t) // builds the fixture home in $HOME
	prev := newSwitcher
	newSwitcher = func(opts store.Options) (*core.Switcher, error) {
		opts.Keychain, opts.OAuth, opts.WinCred, opts.Stderr = keychain.NewFake(), &oauth.FakeClient{}, wincred.NewFake(), io.Discard
		return core.New(opts)
	}
	t.Cleanup(func() { newSwitcher = prev })
	geteuidPrev := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = geteuidPrev })
	withPrompter(t, &fakePrompter{answer: "y", ok: true})

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

	code, out, errStr := runCLI(t, []string{"switch", "3", "--json", "--yes"}, false, false)
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
