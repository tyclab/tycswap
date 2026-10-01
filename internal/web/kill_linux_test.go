//go:build linux

package web

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeProc builds a procfs with one process: boot at bootSecs, pid started
// startTicks clock ticks later, with a command name holding spaces and a
// parenthesis (the splitting trap).
func fakeProc(t *testing.T, pid int, bootSecs int64, startTicks uint64) {
	t.Helper()
	root := t.TempDir()
	prev := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = prev })
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("cpu  1 2 3 4\nbtime "+strconv.FormatInt(bootSecs, 10)+"\nprocesses 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, strconv.Itoa(pid)), 0o700); err != nil {
		t.Fatal(err)
	}
	// Fields 3..22 after the ")" : state ppid pgrp session tty tpgid flags
	// minflt cminflt majflt cmajflt utime stime cutime cstime priority nice
	// num_threads itrealvalue starttime, then a few more.
	line := strconv.Itoa(pid) + " (claude (code) x) S 1 2 3 0 -1 4194560 10 0 0 0 5 5 0 0 20 0 7 0 " + strconv.FormatUint(startTicks, 10) + " 123456 789 18446744073709551615\n"
	if err := os.WriteFile(filepath.Join(root, strconv.Itoa(pid), "stat"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The start time is boot time plus starttime ticks at USER_HZ, parsed past a
// command name with spaces and a parenthesis in it.
func TestProcessStartFromProcfs(t *testing.T) {
	fakeProc(t, 4242, 1_700_000_000, 150*clockTicksPerSecond+50) // 150.5 s after boot
	got, err := processStart(4242)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(1_700_000_000, 0).Add(150*time.Second + 500*time.Millisecond); !got.Equal(want) {
		t.Fatalf("processStart = %v, want %v", got, want)
	}
	if _, err := processStart(4243); err == nil {
		t.Fatal("a pid without a stat file yielded a start time")
	}
}

// The decision over a fake process table: a startedAt within tolerance of
// the process start passes; a stale session file (the PID reused after a
// reboot, or by a later process) and a missing process are ErrNotTheProcess.
func TestCheckStartDecision(t *testing.T) {
	boot := int64(1_700_000_000)
	fakeProc(t, 4242, boot, 1000*clockTicksPerSecond) // started 1000 s after boot
	started := time.Unix(boot+1000, 0)
	if err := checkStart(4242, started.Add(3*time.Second)); err != nil {
		t.Fatalf("a startedAt 3 s after the process start must pass: %v", err)
	}
	if err := checkStart(4242, started.Add(-startTolerance)); err != nil {
		t.Fatalf("exactly the tolerance must pass: %v", err)
	}
	for name, recorded := range map[string]time.Time{
		"the previous boot":        started.Add(-26 * time.Hour),
		"a later process":          started.Add(-startTolerance - time.Second),
		"an earlier process":       started.Add(startTolerance + time.Second),
		"a file from the far past": time.Unix(1, 0),
	} {
		if err := checkStart(4242, recorded); !errors.Is(err, ErrNotTheProcess) {
			t.Errorf("%s: %v, want ErrNotTheProcess", name, err)
		}
	}
	if err := checkStart(4243, started); !errors.Is(err, ErrNotTheProcess) {
		t.Errorf("missing process: %v, want ErrNotTheProcess", err)
	}
}

// Against the real procfs: this test binary started moments ago, so its own
// start time must be recent and in the past.
func TestProcessStartOfSelf(t *testing.T) {
	got, err := processStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if age := time.Since(got); age < 0 || age > 30*time.Minute {
		t.Fatalf("own process start %v is %v ago", got, age)
	}
}
