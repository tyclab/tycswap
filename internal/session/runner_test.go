package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/brand"
)

// childRoleEnv makes the test binary play a probe's child (runTestChild)
// instead of running tests.
var childRoleEnv = brand.Sanitized().EnvPrefix + "_TEST_CHILD"

func TestMain(m *testing.M) {
	if role := os.Getenv(childRoleEnv); role != "" {
		os.Exit(runTestChild(role))
	}
	os.Exit(m.Run())
}

// runTestChild plays a probe's child: "hello" prints hello; "hold-stdout"
// starts a "sleep" grandchild that inherits its stdout, writes the
// grandchild's pid to os.Args[1], prints ready and exits at once.
func runTestChild(role string) int {
	switch role {
	case "hello":
		fmt.Print("hello")
		return 0
	case "hold-stdout":
		exe, err := os.Executable()
		if err != nil {
			return 70
		}
		sleeper := exec.Command(exe)
		sleeper.Env = append(os.Environ(), childRoleEnv+"=sleep")
		sleeper.Stdout = os.Stdout
		if err := sleeper.Start(); err != nil {
			return 70
		}
		if err := os.WriteFile(os.Args[1], []byte(strconv.Itoa(sleeper.Process.Pid)), 0o600); err != nil {
			return 70
		}
		fmt.Println("ready")
		return 0
	case "sleep":
		time.Sleep(30 * time.Second)
		return 0
	}
	return 2
}

// TestProbeGrandchildHoldingPipe pins the WaitDelay + ErrWaitDelay fix: a probe
// whose child exits 0 but leaves a background grandchild holding the captured
// stdout pipe must return after the grace window (probeWaitDelay), not after the
// grandchild's own long sleep. ErrWaitDelay is raised only for a successful exit,
// and the child's stdout was flushed before it exited, so the probe is a SUCCESS
// with the captured output — not a timeout that would delete a valid profile.
//
// This assertion is the inverse of the pre-fix behavior, which mapped
// ErrWaitDelay to DeadlineExceeded and discarded the captured output.
func TestProbeGrandchildHoldingPipe(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The sleeping grandchild inherits the child's stdout (the pipe), so the
	// read side never sees EOF until it exits ~30s later — long past the
	// child's exit. It is killed when the test ends.
	pidFile := filepath.Join(t.TempDir(), "sleeper.pid")
	t.Cleanup(func() {
		raw, _ := os.ReadFile(pidFile)
		if pid, err := strconv.Atoi(string(raw)); err == nil {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})

	const timeout = 10 * time.Second // deadline never fires: parent exits at once
	start := time.Now()
	stdout, rc, err := osRunner{}.Probe([]string{exe, pidFile}, append(os.Environ(), childRoleEnv+"=hold-stdout"), timeout)
	elapsed := time.Since(start)

	// Must return via the grace window, well before the 30s grandchild sleep.
	if elapsed > probeWaitDelay+3*time.Second {
		t.Fatalf("Probe blocked %v on a held pipe; want return near the %v grace window", elapsed, probeWaitDelay)
	}
	if elapsed < probeWaitDelay {
		t.Fatalf("Probe returned after %v, inside the %v grace window: the grandchild held no pipe", elapsed, probeWaitDelay)
	}
	// Successful exit with flushed output: nil error, captured stdout, rc 0.
	if err != nil {
		t.Fatalf("err = %v; want nil (successful exit, stdout already flushed)", err)
	}
	if stdout != "ready\n" || rc != 0 {
		t.Fatalf("stdout=%q rc=%d; want %q/0 (captured output honored)", stdout, rc, "ready\n")
	}
}

// TestClassifyProbeSuccessAtDeadline pins the FINDING 10 fix via the pure
// classify seam: a probe whose process exited cleanly (runErr == nil) exactly as
// the deadline fired must be honored as a success with its full captured output,
// never reclassified as a timeout because ctx has since expired.
func TestClassifyProbeSuccessAtDeadline(t *testing.T) {
	stdout, rc, err := classifyProbe("payload", nil, context.DeadlineExceeded)
	if err != nil || rc != 0 || stdout != "payload" {
		t.Fatalf("classifyProbe(success-at-deadline) = (%q, %d, %v); want (\"payload\", 0, nil)", stdout, rc, err)
	}
}

// TestClassifyProbeErrWaitDelay pins the FINDING 3 fix: ErrWaitDelay (raised only
// on a successful exit whose pipes stayed open) is a success carrying the
// captured output, not a timeout.
func TestClassifyProbeErrWaitDelay(t *testing.T) {
	stdout, rc, err := classifyProbe("ready\n", exec.ErrWaitDelay, nil)
	if err != nil || rc != 0 || stdout != "ready\n" {
		t.Fatalf("classifyProbe(ErrWaitDelay) = (%q, %d, %v); want (%q, 0, nil)", stdout, rc, err, "ready\n")
	}
}

// TestProbeCleanExitCapturesStdout guards the happy path: with WaitDelay set, a
// child that writes and exits cleanly (no leaked pipe) still returns its stdout
// and a nil error.
func TestProbeCleanExitCapturesStdout(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdout, rc, err := osRunner{}.Probe([]string{exe}, append(os.Environ(), childRoleEnv+"=hello"), 10*time.Second)
	if err != nil || rc != 0 || stdout != "hello" {
		t.Fatalf("Probe = (%q, %d, %v); want (\"hello\", 0, nil)", stdout, rc, err)
	}
}
