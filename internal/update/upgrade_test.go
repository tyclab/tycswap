// Upgrader.SelfUpgrade: install-shape dispatch, Windows print-only, and the
// go-install subprocess path (success/failure/not-on-PATH), all via fake
// CommandRunners and fake paths (no real subprocess or filesystem shape is
// touched).
//
// Implements spec 08§13.4 test coverage (run_self_upgrade), redesigned per
// Amendment A6.
package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/platform"
)

// fakeRunner returns a CommandRunner that records the invocation and returns
// the given exit code / error.
func fakeRunner(gotName *string, gotArgs *[]string, exitCode int, err error) CommandRunner {
	return func(ctx context.Context, name string, args []string, stdout, stderr io.Writer) (int, error) {
		*gotName = name
		*gotArgs = append([]string(nil), args...)
		return exitCode, err
	}
}

func goInstallUpgrader(t *testing.T, run CommandRunner) (u Upgrader, exePath string, stdout, stderr *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	exePath = filepath.Join(home, "go", "bin", "tycswap")
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	u = Upgrader{
		Getenv:  func(string) string { return "" }, // no GOBIN/GOPATH -> falls to $HOME/go/bin
		HomeDir: home,
		Run:     run,
		Stdout:  stdout,
		Stderr:  stderr,
	}
	return u, exePath, stdout, stderr
}

// What SelfUpgrade prints for a binary it upgrades by hand, and installs
// nothing: a module build outside a Go bin directory gets main's guidance
// (`go install` or the releases page), a release build in the Nix store the
// package manager, and one this process cannot replace the releases page —
// neither of the release builds `go install`.
func TestSelfUpgrade_UnknownShapePrintsGuidance(t *testing.T) {
	var gotName string
	var gotArgs []string
	upgrader := func() (Upgrader, *bytes.Buffer, *bytes.Buffer) {
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		return Upgrader{
			Getenv:  func(string) string { return "" },
			HomeDir: t.TempDir(),
			Run:     fakeRunner(&gotName, &gotArgs, 0, nil),
			Stdout:  stdout,
			Stderr:  stderr,
		}, stdout, stderr
	}
	unwritable := filepath.Join(t.TempDir(), "missing", "tycswap")
	cases := []struct {
		name    string
		release bool
		exe     string
		want    []string
		not     []string
	}{
		{"module outside a Go bin directory", false, unwritable,
			[]string{"Could not detect a `go install` layout", "go install " + ModulePath + "@latest", ReleasesURL}, nil},
		{"release in the Nix store", true, "/nix/store/0000-tycswap/bin/tycswap",
			[]string{"installed by a package manager: update it with Nix (it is in the Nix store)"}, []string{ReleasesURL, "go install", "git pull"}},
		{"release this process cannot replace", true, unwritable,
			[]string{"Could not upgrade this binary in place", ReleasesURL}, []string{"go install", "git pull"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.release {
				withRelease(t)
			}
			gotName = ""
			u, stdout, stderr := upgrader()
			if code := u.SelfUpgrade(tc.exe, platform.Linux); code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if gotName != "" {
				t.Errorf("ran %s %v; a binary upgraded by hand installs nothing", gotName, gotArgs)
			}
			for _, w := range tc.want {
				if !strings.Contains(stderr.String(), w) {
					t.Errorf("stderr = %q, missing %q", stderr.String(), w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(stderr.String(), n) {
					t.Errorf("stderr = %q, must not say %q", stderr.String(), n)
				}
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout should be empty, got %q", stdout.String())
			}
		})
	}
}

func TestSelfUpgrade_WindowsPrintsOnlyNeverRuns(t *testing.T) {
	var gotName string
	var gotArgs []string
	run := fakeRunner(&gotName, &gotArgs, 0, nil)

	u, exePath, stdout, stderr := goInstallUpgrader(t, run)
	code := u.SelfUpgrade(exePath, platform.Windows)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if gotName != "" {
		t.Error("SelfUpgrade must never run the subprocess on Windows (locked .exe)")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty on the Windows print-only path, got %q", stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "To upgrade tycswap on Windows, run:") {
		t.Errorf("stdout = %q, missing Windows guidance header", got)
	}
	wantCmd := "go install " + ModulePath + "@latest"
	if !strings.Contains(got, wantCmd) {
		t.Errorf("stdout = %q, missing command %q", got, wantCmd)
	}
}

func TestSelfUpgrade_GoInstallShapeRunsAndPropagatesExitCode(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
	}{
		{"success", 0},
		{"nonzero propagates", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotName string
			var gotArgs []string
			run := fakeRunner(&gotName, &gotArgs, tc.exitCode, nil)

			u, exePath, _, stderr := goInstallUpgrader(t, run)
			code := u.SelfUpgrade(exePath, platform.Linux)

			if code != tc.exitCode {
				t.Errorf("exit code = %d, want %d", code, tc.exitCode)
			}
			if gotName != "go" {
				t.Errorf("subprocess name = %q, want go", gotName)
			}
			wantArgs := []string{"install", ModulePath + "@latest"}
			if len(gotArgs) != len(wantArgs) || gotArgs[0] != wantArgs[0] || gotArgs[1] != wantArgs[1] {
				t.Errorf("subprocess args = %v, want %v", gotArgs, wantArgs)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr should be empty on a clean run, got %q", stderr.String())
			}
		})
	}
}

func TestSelfUpgrade_GoNotOnPath(t *testing.T) {
	notFound := &exec.Error{Name: "go", Err: exec.ErrNotFound}
	run := func(ctx context.Context, name string, args []string, stdout, stderr io.Writer) (int, error) {
		return 0, notFound
	}

	u, exePath, stdout, stderr := goInstallUpgrader(t, run)
	code := u.SelfUpgrade(exePath, platform.Linux)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not on PATH") {
		t.Errorf("stderr = %q, want a not-on-PATH message", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got %q", stdout.String())
	}
}

func TestSelfUpgrade_SubprocessStartFailure(t *testing.T) {
	failErr := errors.New("boom")
	run := func(ctx context.Context, name string, args []string, stdout, stderr io.Writer) (int, error) {
		return 0, failErr
	}

	u, exePath, _, stderr := goInstallUpgrader(t, run)
	code := u.SelfUpgrade(exePath, platform.Linux)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr = %q, want the underlying error surfaced", stderr.String())
	}
}

func TestIsNotFound(t *testing.T) {
	if !IsNotFound(&exec.Error{Name: "go", Err: exec.ErrNotFound}) {
		t.Error("IsNotFound should recognize a wrapped exec.ErrNotFound")
	}
	if IsNotFound(errors.New("some other error")) {
		t.Error("IsNotFound should not match an unrelated error")
	}
	if IsNotFound(nil) {
		t.Error("IsNotFound(nil) should be false")
	}
}

// TestRunCommand_RealExec exercises the real CommandRunner end-to-end against
// a harmless binary, proving exit-code propagation and stdout/stderr wiring
// without depending on `go` being on PATH in the test environment.
func TestRunCommand_RealExec(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := RunCommand(context.Background(), "sh", []string{"-c", "echo out; echo err >&2; exit 7"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunCommand error = %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if strings.TrimSpace(stdout.String()) != "out" {
		t.Errorf("stdout = %q, want \"out\"", stdout.String())
	}
	if strings.TrimSpace(stderr.String()) != "err" {
		t.Errorf("stderr = %q, want \"err\"", stderr.String())
	}
}

func TestRunCommand_NotFound(t *testing.T) {
	_, err := RunCommand(context.Background(), "tycswap-definitely-not-a-real-binary-xyz", nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound(err) to be true, err = %v", err)
	}
}
