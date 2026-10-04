// `tycswap upgrade` self-upgrade dispatch.
//
// Implements spec 08§13.4 (run_self_upgrade), redesigned per DESIGN.md §6
// Deviation #2 and Amendments A6/A24/A36: there is no PyPI/uv/pipx for a Go
// binary, so upgrading means re-running `go install <ModulePath>@latest` when
// the running binary lives in a Go-managed bin dir (print-only on Windows,
// where the running .exe is locked — Python's win32 rationale), downloading
// the newest release over the binary when it sits anywhere else it can be
// written (download.go), and printing manual guidance otherwise.
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/printer"
)

// CommandRunner executes a subprocess and reports its exit code. A non-nil
// err means the process could not be started at all (e.g. the binary is
// missing from PATH — check with IsNotFound); a completed process's nonzero
// exit is reported via exitCode with err == nil, mirroring Python's
// subprocess.run(check=False).
type CommandRunner func(ctx context.Context, name string, args []string, stdout, stderr io.Writer) (exitCode int, err error)

// RunCommand is the real CommandRunner: os/exec with no timeout, matching
// Python's un-timed subprocess.run for the upgrade command.
func RunCommand(ctx context.Context, name string, args []string, stdout, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

// IsNotFound reports whether err (as returned by a CommandRunner) indicates
// the target binary is missing from PATH.
func IsNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}

// Upgrader implements `tycswap upgrade`. The zero value uses the real OS
// environment, subprocess execution, and stdio; tests override every seam.
type Upgrader struct {
	// Getenv looks up GOBIN/GOPATH for install-shape detection; nil -> os.Getenv.
	Getenv func(string) string
	// HomeDir is $HOME for install-shape detection; "" -> os.UserHomeDir().
	HomeDir string
	// Run executes the `go install` subprocess; nil -> RunCommand.
	Run CommandRunner
	// Stdout/Stderr receive guidance text and the subprocess's own output;
	// nil -> os.Stdout / os.Stderr.
	Stdout, Stderr io.Writer
	// The download shape (download.go): Version is the running build's
	// v-prefixed semver ("" → always install the newest); Arch overrides
	// runtime.GOARCH; HTTPClient nil → http.DefaultClient. The release
	// endpoint and downloads are the package's Endpoint and ReleasesURL.
	Version    string
	Arch       string
	HTTPClient *http.Client
}

func (u Upgrader) getenv() func(string) string {
	if u.Getenv != nil {
		return u.Getenv
	}
	return os.Getenv
}

func (u Upgrader) homeDir() string {
	if u.HomeDir != "" {
		return u.HomeDir
	}
	h, _ := os.UserHomeDir()
	return h
}

func (u Upgrader) run() CommandRunner {
	if u.Run != nil {
		return u.Run
	}
	return RunCommand
}

func (u Upgrader) stdout() io.Writer {
	if u.Stdout != nil {
		return u.Stdout
	}
	return os.Stdout
}

func (u Upgrader) stderr() io.Writer {
	if u.Stderr != nil {
		return u.Stderr
	}
	return os.Stderr
}

// SelfUpgrade runs the upgrade UpgradePlan picks for the running binary and
// returns the process exit code (never an error — every failure path prints
// guidance and returns 1, matching run_self_upgrade's contract of "return an
// int, don't raise").
//
// exePath is the running binary's path (symlink-resolved os.Executable());
// plat gates the Windows print-only branch.
func (u Upgrader) SelfUpgrade(exePath string, plat platform.Platform) int {
	cmdArgs := []string{"install", ModulePath + "@latest"}
	fullCmd := "go " + strings.Join(cmdArgs, " ")
	binary := exePath
	if binary == "" {
		binary = "(unknown)"
	}
	src := DetectBuildSource()
	plan := UpgradePlan(src, exePath, u.getenv(), u.homeDir())
	switch plan.Method {
	case MethodCheckout:
		// Never re-installed from a remote: `go install <ModulePath>@latest`
		// would replace the user's own tree with whatever is published
		// there (Amendment A24).
		fmt.Fprintf(u.stdout(), "tycswap was %s\n", CheckoutHint)
		return 1
	case MethodDownload:
		return u.downloadUpgrade(exePath, plat)
	case MethodPackageManager:
		fmt.Fprintf(u.stderr(),
			"This tycswap was installed by a package manager: update it with %s.\n"+
				"  binary: %s\n",
			plan.Updater(), binary)
		return 1
	case MethodGoInstallElsewhere:
		fmt.Fprintf(u.stderr(),
			"Could not detect a `go install` layout (looked for $GOBIN, $GOPATH/bin, $HOME/go/bin).\n"+
				"  binary: %s\n"+
				"To upgrade manually, run:\n"+
				"  %s\n"+
				"Or download a release from:\n"+
				"  %s\n",
			binary, fullCmd, ReleasesURL)
		return 1
	case MethodManual:
		fmt.Fprintf(u.stderr(),
			"Could not upgrade this binary in place: this process cannot replace it (its directory or the file\n"+
				"cannot be written, or it belongs to another user).\n"+
				"  binary: %s\n"+
				"To upgrade manually, download the build for this machine from:\n"+
				"  %s\n",
			binary, ReleasesURL)
		return 1
	}

	// Windows: the running tycswap.exe is locked, so `go install` cannot replace
	// it in place even though the module itself would update fine (spec
	// 08§13.4's win32 rationale, carried over). Print the command instead of
	// running it; tycswap exits right after this, releasing the lock.
	if plat == platform.Windows {
		fmt.Fprintf(u.stdout(), "To upgrade tycswap on Windows, run:\n  %s\n", printer.Accent(fullCmd))
		return 1
	}

	code, err := u.run()(context.Background(), "go", cmdArgs, u.stdout(), u.stderr())
	if err != nil {
		if IsNotFound(err) {
			fmt.Fprintln(u.stderr(),
				"Detected a go install layout but `go` is not on PATH. "+
					"Run the upgrade manually from a shell where it is available.")
			return 1
		}
		fmt.Fprintln(u.stderr(), err.Error())
		return 1
	}
	return code
}
