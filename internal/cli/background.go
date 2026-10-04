// background.go — bare `tycswap` starts the menu-bar / tray app in the
// background and gives the terminal back (DESIGN A40), and `tycswap app
// --detach` does the same with the flags it was given.
//
// The app is what people want when they type the command, and holding a
// terminal open for a menu-bar icon is not. So the bare command spawns
// `tycswap app` as a detached process — its own session on Unix, no console
// on Windows — with its output appended to a log file, waits until that
// process holds the single-instance lock (A38), and exits. `app` itself
// still runs in the foreground: start-at-login entries and anyone debugging
// run it that way. The terminal UI stays reachable as `tycswap tui`.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/autostart"
	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/paths"
)

// backgroundApp is the running detached child as startBackgroundApp needs
// it: whether it has already exited (with its error).
type backgroundApp interface {
	Exited() (bool, error)
}

// spawnBackgroundApp starts `<exe> <args…>` detached, with stdin on the null
// device and stdout/stderr appended to logPath. A seam for tests.
var spawnBackgroundApp = spawnDetachedApp

// lockRunning is the single-instance probe startBackgroundApp polls, at the
// lock the child takes; a seam for tests.
var lockRunning = lockHeld

// Timing of the start confirmation. The child takes the lock as the first
// thing `app` does, so seconds are generous; a machine under heavy load gets
// the benefit of the doubt rather than a false "failed".
var (
	backgroundStartWait = 8 * time.Second
	backgroundPoll      = 100 * time.Millisecond
)

// appLogPath is where the background app's output goes: the file the macOS
// LaunchAgent already logs to, so both ways of starting the app share one log
// (A35), and the backup root elsewhere.
func appLogPath() string {
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return autostart.LogPath(home)
		}
	}
	return filepath.Join(paths.GetBackupRoot(), "app.log")
}

// maxAppLog is the size at which the log is rotated to <log>.1 before a new
// background start appends to it.
const maxAppLog = 5 << 20

// openAppLog opens logPath for appending, rotating it once it is large.
func openAppLog(logPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > maxAppLog {
		_ = os.Rename(logPath, logPath+".1")
	}
	return os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// startBackgroundApp is bare `tycswap` in a terminal: `app` with no flags,
// confirmed through app.lock.
func startBackgroundApp(prog string, s ioStreams) int {
	return startDetached(prog, []string{"app"}, appLockPath(), s)
}

// startDetached spawns `<exe> <args…>` detached and returns once the child
// holds lockPath — app.lock for the local app, remote.lock for a remote
// tray — or has exited, or is still starting after backgroundStartWait.
func startDetached(prog string, args []string, lockPath string, s ioStreams) int {
	name := brand.Sanitized().Name
	if lockRunning(lockPath) {
		fmt.Fprintln(s.out, name+" is already running — its icon is in the menu bar / tray.")
		return 0
	}
	exe := exePath()
	if exe == "" {
		errorTo(s.err, "Could not locate the "+name+" binary; start it with `"+prog+" "+strings.Join(args, " ")+"`.")
		return 1
	}
	logPath := appLogPath()
	child, err := spawnBackgroundApp(exe, args, logPath)
	if err != nil {
		errorTo(s.err, "Could not start "+name+" in the background: "+err.Error()+". Run `"+prog+" "+strings.Join(args, " ")+"` to see it in the foreground.")
		return 1
	}
	deadline := time.Now().Add(backgroundStartWait)
	for {
		if lockRunning(lockPath) {
			fmt.Fprintln(s.out, name+" is running in the background — its icon is in the menu bar / tray.")
			fmt.Fprintln(s.out, "Log: "+logPath)
			return 0
		}
		if done, werr := child.Exited(); done {
			msg := name + " stopped right after starting"
			if werr != nil {
				msg += " (" + werr.Error() + ")"
			}
			errorTo(s.err, msg+".")
			if tail := logTail(logPath, 5); tail != "" {
				fmt.Fprintln(s.err, tail)
			}
			fmt.Fprintln(s.err, "Full log: "+logPath)
			return 1
		}
		if time.Now().After(deadline) {
			// Still starting (or wedged before it took the lock). It is
			// detached either way; say where to look instead of guessing.
			fmt.Fprintln(s.out, name+" is starting in the background; if no icon appears, see "+logPath+".")
			return 0
		}
		time.Sleep(backgroundPoll)
	}
}

// logTail returns the last n non-empty lines of path, "" when unreadable.
func logTail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const window = 16 << 10
	if fi, err := f.Stat(); err == nil && fi.Size() > window {
		if _, err := f.Seek(fi.Size()-window, io.SeekStart); err != nil {
			return ""
		}
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// processApp adapts a spawned *os.Process: Wait runs on a goroutine so
// Exited never blocks.
type processApp struct {
	done chan struct{}
	err  error
}

func watchProcess(p *os.Process) *processApp {
	a := &processApp{done: make(chan struct{})}
	go func() {
		st, err := p.Wait()
		switch {
		case err != nil:
			a.err = err
		case !st.Success():
			a.err = errors.New(st.String())
		}
		close(a.done)
	}()
	return a
}

func (a *processApp) Exited() (bool, error) {
	select {
	case <-a.done:
		return true, a.err
	default:
		return false, nil
	}
}
