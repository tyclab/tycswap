package cli

import (
	"bytes"
	"strings"
	"testing"
)

func runSub(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run("tycswap", argv, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	return code, out.String(), errb.String()
}

// TestAliasArgValidation pins the three exit-2 argument errors (spec 08§7.4).
// These fire before switcher construction, so no home is needed.
func TestAliasArgValidation(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		msg  string
	}{
		{"unset with name", []string{"alias", "2", "dev", "--unset"}, "--unset does not take a NAME argument"},
		{"unset without target", []string{"alias", "--unset"}, "NUM|EMAIL is required with --unset"},
		{"set without name", []string{"alias", "2"}, "NAME is required (or pass --unset to remove the alias)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errStr := runSub(t, tc.argv...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr=%q)", code, errStr)
			}
			if !strings.Contains(errStr, tc.msg) {
				t.Errorf("stderr = %q, want %q", errStr, tc.msg)
			}
		})
	}
}

// TestSwapMissingArgs: swap requires two positionals (exit 2). The message lists
// only the still-missing metavars, mirroring argparse (spec 08§7.5).
func TestSwapMissingArgs(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		msg  string
	}{
		{"one arg", []string{"swap", "1"}, "the following arguments are required: NUM|EMAIL|ALIAS"},
		{"no args", []string{"swap"}, "the following arguments are required: NUM|EMAIL|ALIAS, NUM|EMAIL|ALIAS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errStr := runSub(t, tc.argv...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr=%q)", code, errStr)
			}
			if !strings.Contains(errStr, tc.msg) {
				t.Errorf("stderr = %q, want %q", errStr, tc.msg)
			}
		})
	}
}

// TestMoveMissingArgs: move requires account + slot (exit 2). The message uses
// the metavars NUM|EMAIL|ALIAS / SLOT and lists only the still-missing ones,
// mirroring argparse (spec 08§7.6).
func TestMoveMissingArgs(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		msg  string
	}{
		{"one arg", []string{"move", "2"}, "the following arguments are required: SLOT"},
		{"no args", []string{"move"}, "the following arguments are required: NUM|EMAIL|ALIAS, SLOT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errStr := runSub(t, tc.argv...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr=%q)", code, errStr)
			}
			if !strings.Contains(errStr, tc.msg) {
				t.Errorf("stderr = %q, want %q", errStr, tc.msg)
			}
		})
	}
}

// TestRunTailSplit: `run 2 -- --bogus` forwards the tail unparsed; a bad flag
// BEFORE the "--" is an exit-2 error. Both go through runCommand's head parse.
func TestRunBadFlagBeforeDashDash(t *testing.T) {
	cleanHome(t)
	code, _, errStr := runSub(t, "run", "2", "--bogus")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%q)", code, errStr)
	}
	if !strings.Contains(errStr, "unrecognized arguments") {
		t.Errorf("stderr = %q, want 'unrecognized arguments'", errStr)
	}
}

// TestRunExtraPositional: two positional accounts is an exit-2 error.
func TestRunExtraPositional(t *testing.T) {
	cleanHome(t)
	code, _, errStr := runSub(t, "run", "2", "3")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%q)", code, errStr)
	}
	if !strings.Contains(errStr, "unrecognized arguments") {
		t.Errorf("stderr = %q, want 'unrecognized arguments'", errStr)
	}
}

// TestAutoBadValue: a non-numeric --seven-day-threshold is an exit-2 error (before the
// switcher is built).
func TestAutoBadThreshold(t *testing.T) {
	code, _, errStr := runSub(t, "auto", "--seven-day-threshold", "high")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%q)", code, errStr)
	}
	if !strings.Contains(errStr, "invalid float value") {
		t.Errorf("stderr = %q, want 'invalid float value'", errStr)
	}
}

// TestAutoIncludeAPIKeyFlagsAreGone: the --include-api-key-accounts pair went
// with its setting (DESIGN A33); either spelling is an unrecognized argument.
func TestAutoIncludeAPIKeyFlagsAreGone(t *testing.T) {
	for _, flag := range []string{"--include-api-key-accounts", "--no-include-api-key-accounts"} {
		code, _, errStr := runSub(t, "auto", flag)
		if code != 2 || !strings.Contains(errStr, "unrecognized arguments: "+flag) {
			t.Errorf("%s: exit %d, stderr %q; want exit 2, unrecognized", flag, code, errStr)
		}
	}
	code, out, _ := runSub(t, "auto", "--help")
	if code != 0 || strings.Contains(out, "include-api-key") || !strings.Contains(out, "never moves onto") {
		t.Errorf("auto --help (exit %d) = %q", code, out)
	}
}

// TestAutoThresholdFlagsPerWindow: one flag per bar (DESIGN A34); each takes
// a number, and the single --threshold is gone.
func TestAutoThresholdFlagsPerWindow(t *testing.T) {
	for _, flag := range []string{"--five-hour-threshold", "--seven-day-threshold", "--model-threshold"} {
		code, _, errStr := runSub(t, "auto", flag, "high")
		if code != 2 || !strings.Contains(errStr, "argument "+flag+": invalid float value: 'high'") {
			t.Errorf("%s high: exit %d, stderr %q", flag, code, errStr)
		}
		code, _, errStr = runSub(t, "auto", flag)
		if code != 2 || !strings.Contains(errStr, "argument "+flag+": expected one argument") {
			t.Errorf("%s: exit %d, stderr %q", flag, code, errStr)
		}
	}
	code, _, errStr := runSub(t, "auto", "--threshold", "80")
	if code != 2 || !strings.Contains(errStr, "unrecognized arguments: --threshold") {
		t.Errorf("--threshold: exit %d, stderr %q", code, errStr)
	}
	code, out, _ := runSub(t, "auto", "--help")
	for _, want := range []string{"--five-hour-threshold PCT", "--seven-day-threshold PCT", "--model-threshold PCT", "Each window has a bar of its own"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("auto --help (exit %d) lacks %q:\n%s", code, want, out)
		}
	}
}

// TestAutoUnknownFlag: an unknown auto flag is an exit-2 error.
func TestAutoUnknownFlag(t *testing.T) {
	code, _, errStr := runSub(t, "auto", "--bogus")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%q)", code, errStr)
	}
	if !strings.Contains(errStr, "unrecognized arguments") {
		t.Errorf("stderr = %q, want 'unrecognized arguments'", errStr)
	}
}
