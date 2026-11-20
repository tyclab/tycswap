//go:build linux

package cli

import (
	"bytes"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/testutil"
	"golang.org/x/sys/unix"
)

// openTerminal is the terminal end of a new pseudo-terminal and its other end.
func openTerminal(t *testing.T) (tty, master *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	n, err := unix.IoctlGetUint32(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	tty, err = os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = tty.Close() })
	return tty, master
}

// A terminal on stdin is not read, though it holds a status line that would
// print: start --check prints nothing and exits 0 (DESIGN A108).
func TestStartCheckReadsNoTerminal(t *testing.T) {
	fableStart(t, "on")
	testutil.Unsetenv(t, secureStorageVar)
	tty, master := openTerminal(t)
	if _, err := master.Write([]byte(statusLine(checkSession, "claude-fable-5-1") + "\n")); err != nil {
		t.Fatal(err)
	}
	type result struct {
		code     int
		out, err string
	}
	done := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		code := run("tycswap", []string{"start", "--check"}, ioStreams{in: tty, out: &out, err: &errb}, true, false)
		done <- result{code, out.String(), errb.String()}
	}()
	select {
	case r := <-done:
		if r.code != 0 || r.out != "" || r.err != "" {
			t.Fatalf("code=%d out=%q err=%q", r.code, r.out, r.err)
		}
	case <-time.After(10 * time.Second):
		_ = master.Close()
		t.Fatal("start --check waited on the terminal")
	}
}
