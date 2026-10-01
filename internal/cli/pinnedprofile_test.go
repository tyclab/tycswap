package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

// makeSessionProfile creates a tycswap session profile dir under the current
// backup root and returns its path (the value a `tycswap env`-pinned shell carries
// in CLAUDE_CONFIG_DIR).
func makeSessionProfile(t *testing.T) string {
	t.Helper()
	dir := sessprofile.SessionDirFor(paths.GetBackupRoot(), "2", "user@example.com")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestNeutralizePinnedSessionProfile: when CLAUDE_CONFIG_DIR points at a tycswap
// session profile, the front-controller helper unsets it and prints the one D2
// note. A non-env/run command then resolves the default login.
func TestNeutralizePinnedSessionProfile(t *testing.T) {
	cleanHome(t)
	profile := makeSessionProfile(t)
	os.Setenv("CLAUDE_CONFIG_DIR", profile)
	t.Cleanup(func() { os.Unsetenv("CLAUDE_CONFIG_DIR") })

	var errb bytes.Buffer
	neutralizePinnedSessionProfile(&errb)

	if got := os.Getenv("CLAUDE_CONFIG_DIR"); got != "" {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want unset", got)
	}
	if !strings.Contains(errb.String(), "pinned via tycswap env; operating on the default login") {
		t.Errorf("missing D2 note: %q", errb.String())
	}
}

// TestNeutralizeCustomConfigDirHonored: a custom, non-tycswap CLAUDE_CONFIG_DIR is
// left untouched (Python parity) — no note, value preserved.
func TestNeutralizeCustomConfigDirHonored(t *testing.T) {
	cleanHome(t)
	custom := filepath.Join(t.TempDir(), "my-config")
	os.Setenv("CLAUDE_CONFIG_DIR", custom)
	t.Cleanup(func() { os.Unsetenv("CLAUDE_CONFIG_DIR") })

	var errb bytes.Buffer
	neutralizePinnedSessionProfile(&errb)

	if got := os.Getenv("CLAUDE_CONFIG_DIR"); got != custom {
		t.Errorf("custom CLAUDE_CONFIG_DIR = %q, want preserved %q", got, custom)
	}
	if errb.Len() != 0 {
		t.Errorf("custom config dir produced a note: %q", errb.String())
	}
}

// TestNeutralizeNoConfigDir: no CLAUDE_CONFIG_DIR set is a silent no-op.
func TestNeutralizeNoConfigDir(t *testing.T) {
	cleanHome(t)
	var errb bytes.Buffer
	neutralizePinnedSessionProfile(&errb)
	if errb.Len() != 0 {
		t.Errorf("unset CLAUDE_CONFIG_DIR produced output: %q", errb.String())
	}
}

// TestRunNeutralizesForNonEnvRun: a non-env/run command dispatched through run()
// clears a pinned CLAUDE_CONFIG_DIR and notes it; env/run keep the pin (they own
// their own preset handling).
func TestRunNeutralizesForNonEnvRun(t *testing.T) {
	t.Run("list neutralizes", func(t *testing.T) {
		cleanHome(t)
		profile := makeSessionProfile(t)
		os.Setenv("CLAUDE_CONFIG_DIR", profile)
		t.Cleanup(func() { os.Unsetenv("CLAUDE_CONFIG_DIR") })

		var out, errb bytes.Buffer
		code := run("tycswap", []string{"list"}, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr=%q)", code, errb.String())
		}
		if !strings.Contains(errb.String(), "pinned via tycswap env") {
			t.Errorf("list did not emit the D2 note: %q", errb.String())
		}
		if got := os.Getenv("CLAUDE_CONFIG_DIR"); got != "" {
			t.Errorf("list left CLAUDE_CONFIG_DIR = %q, want unset", got)
		}
	})

	t.Run("env keeps the pin", func(t *testing.T) {
		cleanHome(t)
		profile := makeSessionProfile(t)
		os.Setenv("CLAUDE_CONFIG_DIR", profile)
		t.Cleanup(func() { os.Unsetenv("CLAUDE_CONFIG_DIR") })

		// `env --unset` returns before switcher construction; run() must NOT
		// neutralize (env owns preset handling), so no note and the pin survives.
		var out, errb bytes.Buffer
		code := run("tycswap", []string{"env", "--unset"}, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr=%q)", code, errb.String())
		}
		if strings.Contains(errb.String(), "pinned via tycswap env") {
			t.Errorf("env emitted the D2 neutralization note: %q", errb.String())
		}
		if got := os.Getenv("CLAUDE_CONFIG_DIR"); got != profile {
			t.Errorf("env cleared the pin (CLAUDE_CONFIG_DIR=%q); it must keep it", got)
		}
	})
}
