package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/store"
	"golang.org/x/sys/unix"
)

func TestStatusLineDefersDuringClientRefresh(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := linkedTestStart(t, rec, placementSession, "opus")
	if err := os.Mkdir(filepath.Join(link, ".oauth_refresh.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	line := sessionStatus(t, link, placementSession, "claude-fable-5-2", false)
	if took := time.Since(start); took > time.Second || !strings.Contains(line, ".oauth_refresh.lock") {
		t.Fatalf("busy status line took %s and reported %q", took, line)
	}
	checkLinkTarget(t, link, sw.DefaultProfileDir())
}

func TestTickDefersDuringClientRefresh(t *testing.T) {
	sw, from, to := mythosTickFixture(t, 100, 30, false)
	link := bindTickSession(t, sw, tickSessionA, "mythos", from)
	lock := filepath.Join(from, ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, out, errText := runCodex(t, "", "auto", "--once", "--json")
	if took := time.Since(start); took > time.Second || !strings.Contains(out, `"reason":"mythos-session"`) || !strings.Contains(out, ".oauth_refresh.lock") {
		t.Fatalf("busy tick took %s: %s %s", took, out, errText)
	}
	requireLinkTarget(t, link, from)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	runCodex(t, "", "auto", "--once", "--json")
	requireLinkTarget(t, link, to)
}

func TestUseWaitAllowsOtherCommandsAndRechecksDestination(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", off: true})
	link := linkedTestStart(t, rec, placementSession, "opus")
	root := sw.BackupDir()
	setRecord(t, sw, "2", "mythos", true)
	dir := store.MythosLoginDir(root, fableEmail("2"), "")
	writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
	lock := filepath.Join(sw.DefaultProfileDir(), ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	// Synchronize with use reading the binding before starting the second command.
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := unix.InotifyAddWatch(fd, filepath.Join(root, "default_models.json"), unix.IN_CLOSE_NOWRITE); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	var notice bytes.Buffer
	go func() {
		done <- useCommand("tycswap", []string{"mythos", "--session", placementSession}, ioStreams{out: io.Discard, err: &notice})
	}()
	defer func() {
		_ = os.Remove(lock)
		if done != nil {
			<-done
		}
	}()
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 2000); n != 1 || err != nil {
		t.Fatalf("use did not read the binding: %d, %v", n, err)
	}
	// This lock protects other sessions; it must be available while use is waiting, too.
	models := filelock.New(filepath.Join(root, ".default_models.lock"), time.Second)
	if ok, err := models.Acquire(time.Second); !ok || err != nil {
		t.Errorf("use held the session-record lock while waiting: %v", err)
	} else {
		models.Release()
	}
	start := time.Now()
	var commandNotice bytes.Buffer
	code := mythosCommand("tycswap", []string{"--disallow", "2"}, ioStreams{out: io.Discard, err: &commandNotice})
	if took := time.Since(start); code != 0 || took > time.Second {
		t.Fatalf("mythos --disallow took %s: exit %d, %q", took, code, commandNotice.String())
	}
	select {
	case code := <-done:
		done = nil
		t.Fatalf("use returned before the client released its lock: %d, %q", code, notice.String())
	default:
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	code = <-done
	done = nil
	if code != 1 || !strings.Contains(strings.ToLower(notice.String()), "mythos") {
		t.Fatalf("use did not recheck the unmarked login: %d, %q", code, notice.String())
	}
	checkLinkTarget(t, link, sw.DefaultProfileDir())
}
