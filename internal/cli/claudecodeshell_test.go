package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccversion"
)

// brewSeat is a Claude Code check on a Mac whose Claude Code is the Homebrew
// cask at version, with latest the newest version its installer offers ("" for
// none known).
func brewSeat(version, latest string) ccversion.Status {
	return ccversion.Status{
		Installed: &ccversion.Installed{
			Path: "/opt/homebrew/bin/claude", Real: "/opt/homebrew/Caskroom/claude-code/" + version + "/claude",
			Version: version, Method: ccversion.Homebrew, Cask: "claude-code",
		},
		Latest:  latest,
		Checked: true,
	}
}

// claudeShell is a test shell whose Claude Code commands are recorded, and
// whose dialog answers yes (or no). The run brings Claude Code to the newest
// version; the check after it says so.
func claudeShell(t *testing.T, answer bool) (*appShell, *fakeTray, *[]string) {
	t.Helper()
	sh, ft, calls := newTestShell(t)
	current := brewSeat("2.1.281", "2.1.281")
	sh.act.Ask = func(title, body, ok, cancel string) (bool, error) {
		*calls = append(*calls, "ask:"+title)
		return answer, nil
	}
	sh.act.RunClaudeCode = func(c ccversion.Command, in *ccversion.Installed) (string, error) {
		*calls = append(*calls, "run:"+c.String())
		return "==> Upgrading claude-code\n", nil
	}
	sh.act.CheckClaudeCode = func() ccversion.Status {
		*calls = append(*calls, "recheck")
		return current
	}
	sh.update(sampleState())
	return sh, ft, calls
}

func hasCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// An update shows as a row and is announced once per version; the click asks,
// then runs the installer's own upgrade and looks again.
func TestClaudeCodeUpdateRow(t *testing.T) {
	sh, ft, calls := claudeShell(t, true)
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	it, ok := ft.item("claude-code")
	if !ok || it.Title != "Update Claude Code 2.1.280 → 2.1.281…" || it.Disabled || it.Sub != "with Homebrew · running sessions keep the old one" {
		t.Fatalf("row = %+v (present %v)", it, ok)
	}
	if len(ft.notes) != 1 || !strings.Contains(ft.notes[0], "Claude Code 2.1.281 is available") {
		t.Errorf("notes = %v", ft.notes)
	}
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281")) // the next check: no second announcement
	if len(ft.notes) != 1 {
		t.Errorf("announced twice: %v", ft.notes)
	}
	sh.click("claude-code")
	for _, want := range []string{"ask:Update Claude Code?", "run:brew upgrade --cask claude-code", "recheck"} {
		if !hasCall(*calls, want) {
			t.Errorf("calls %v lack %q", *calls, want)
		}
	}
	if last := ft.notes[len(ft.notes)-1]; !strings.Contains(last, "update done") {
		t.Errorf("last note = %q", last)
	}
}

// "Cancel" runs nothing.
func TestClaudeCodeUpdateCancelled(t *testing.T) {
	sh, _, calls := claudeShell(t, false)
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	sh.click("claude-code")
	if hasCall(*calls, "run:") {
		t.Errorf("ran after Cancel: %v", *calls)
	}
}

// A failed upgrade reports the command's last line.
func TestClaudeCodeUpdateFails(t *testing.T) {
	sh, ft, _ := claudeShell(t, true)
	sh.act.RunClaudeCode = func(ccversion.Command, *ccversion.Installed) (string, error) {
		return "Error: claude-code: Download failed\n", errors.New("exit status 1")
	}
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	sh.click("claude-code")
	if last := ft.notes[len(ft.notes)-1]; !strings.Contains(last, "update failed | Error: claude-code: Download failed") {
		t.Errorf("last note = %q", last)
	}
}

// A zero exit status is not success on its own (A27): a Claude Code that is
// still at the old version after the run is reported.
func TestClaudeCodeUpdateVerifiesTheVersion(t *testing.T) {
	sh, ft, _ := claudeShell(t, true)
	sh.act.CheckClaudeCode = func() ccversion.Status { return brewSeat("2.1.280", "2.1.281") }
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	sh.click("claude-code")
	if last := ft.notes[len(ft.notes)-1]; !strings.Contains(last, "update failed | the installer finished, but Claude Code is still at 2.1.280 (expected 2.1.281 or newer)") {
		t.Errorf("last note = %q", last)
	}
}

func TestClaudeCodeRowStates(t *testing.T) {
	missing := brewSeat("", "2.1.281")
	missing.Installed = nil
	failed := brewSeat("2.1.280", "")
	failed.Err = errors.New("HTTP 502")
	for _, tc := range []struct {
		name     string
		st       ccversion.Status
		title    string
		disabled bool
	}{
		{"latest", brewSeat("2.1.281", "2.1.281"), "Claude Code 2.1.281 — latest", true},
		{"missing", missing, "Claude Code is not installed — how to install…", false},
		{"cannot check", failed, "Claude Code 2.1.280 — could not check for updates", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh, ft, _ := claudeShell(t, true)
			sh.setClaudeCode(tc.st)
			it, ok := ft.item("claude-code")
			if !ok || it.Title != tc.title || it.Disabled != tc.disabled {
				t.Errorf("row = %+v (present %v), want %q disabled=%v", it, ok, tc.title, tc.disabled)
			}
			if len(ft.notes) != 0 {
				t.Errorf("only a new version is announced; got %v", ft.notes)
			}
		})
	}
}

// A missing Claude Code is never installed from the tray (A27): the click
// says the line to type and runs nothing.
func TestClaudeCodeMissingShowsTheInstallLine(t *testing.T) {
	sh, ft, calls := claudeShell(t, true)
	st := brewSeat("", "")
	st.Installed = nil
	sh.setClaudeCode(st)
	sh.click("claude-code")
	if hasCall(*calls, "run:") || hasCall(*calls, "ask:") {
		t.Errorf("calls = %v", *calls)
	}
	if len(ft.notes) != 1 || !strings.HasPrefix(ft.notes[0], "Install Claude Code | Claude Code is not installed on this machine. Install it with: ") ||
		!strings.Contains(ft.notes[0], ccversion.InstallHint(platformGOOS())) {
		t.Errorf("notes = %v", ft.notes)
	}
}

// No check yet, or no way to run commands: no row.
func TestClaudeCodeRowHidden(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.update(sampleState())
	if _, ok := ft.item("claude-code"); ok {
		t.Error("row shown before any check")
	}
	sh.setClaudeCode(brewSeat("2.1.280", "2.1.281"))
	if _, ok := ft.item("claude-code"); ok {
		t.Error("row shown without RunClaudeCode")
	}
}

func brandSub(t *testing.T, ft *fakeTray) string {
	t.Helper()
	it, ok := ft.item("brand")
	if !ok {
		t.Fatal("no brand row")
	}
	return it.Sub
}

// The brand row names Claude Code on its own line: nothing before the app
// has looked, the version from the quick look at start, and after a full
// check whether it is the latest. Without RunClaudeCode too: it is only text.
func TestBrandRowNamesClaudeCode(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.update(sampleState())
	if sub := brandSub(t, ft); strings.Contains(sub, "\n") {
		t.Errorf("before any look: %q", sub)
	}
	sh.setClaudeCodeInstalled(brewSeat("2.1.280", "").Installed)
	if sub := brandSub(t, ft); !strings.HasSuffix(sub, "\nClaude Code 2.1.280") {
		t.Errorf("after the quick look: %q", sub)
	}
	for _, tc := range []struct {
		st   ccversion.Status
		want string
	}{
		{brewSeat("2.1.280", "2.1.281"), "\nClaude Code 2.1.280 · 2.1.281 available"},
		{brewSeat("2.1.281", "2.1.281"), "\nClaude Code 2.1.281 · latest"},
		{brewSeat("2.1.281", ""), "\nClaude Code 2.1.281"},
	} {
		sh.setClaudeCode(tc.st)
		if sub := brandSub(t, ft); !strings.HasSuffix(sub, tc.want) || !strings.Contains(sub, "auto-switch") {
			t.Errorf("brand = %q, want suffix %q", sub, tc.want)
		}
	}
	sh.setClaudeCodeInstalled(nil) // a late quick look does not undo the full check
	if sub := brandSub(t, ft); !strings.HasSuffix(sub, "\nClaude Code 2.1.281") {
		t.Errorf("late quick look won: %q", sub)
	}
}

func TestBrandRowClaudeCodeMissing(t *testing.T) {
	sh, ft, _ := newTestShell(t)
	sh.update(sampleState())
	sh.setClaudeCodeInstalled(nil)
	if sub := brandSub(t, ft); !strings.HasSuffix(sub, "\nClaude Code not installed") {
		t.Errorf("brand = %q", sub)
	}
}
