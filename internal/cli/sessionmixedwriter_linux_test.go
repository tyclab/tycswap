package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"golang.org/x/sys/unix"
)

func TestUseRepairsBindingStrippedWhileWaitingForRefresh(t *testing.T) {
	sw, launch := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := linkedTestStart(t, launch, placementSession, "opus")
	root := sw.BackupDir()
	sessionRegistryEntry(t, placementSession, "")

	sourceGuard := filepath.Join(sw.DefaultProfileDir(), ".oauth_refresh.lock")
	if err := os.Mkdir(sourceGuard, 0700); err != nil {
		t.Fatal(err)
	}
	records := filepath.Join(root, "default_models.json")
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := unix.InotifyAddWatch(fd, records, unix.IN_CLOSE_NOWRITE); err != nil {
		t.Fatal(err)
	}

	// The real 0.19.0 writer re-encodes the entire map using these four row fields.
	type legacyRow struct {
		Family  string  `json:"family"`
		SeenAt  float64 `json:"seenAt"`
		Store   string  `json:"store,omitempty"`
		Binding string  `json:"binding,omitempty"`
	}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- useCommand("tycswap", []string{"fable", "--session", placementSession}, ioStreams{out: io.Discard, err: &stderr})
	}()
	t.Cleanup(func() {
		_ = os.Remove(sourceGuard)
		if done != nil {
			<-done
		}
	})
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 3000); n != 1 || err != nil {
		t.Fatalf("use did not read its binding: %d, %v", n, err)
	}
	lock := filelock.New(filepath.Join(root, ".default_models.lock"), time.Second)
	if err := lock.With(func() error {
		raw, err := os.ReadFile(records)
		if err != nil {
			return err
		}
		var snapshot struct {
			Schema   int                  `json:"schemaVersion"`
			Sessions map[string]legacyRow `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		raw, err = json.Marshal(snapshot)
		if err != nil {
			return err
		}
		return atomicfile.Write(records, raw, atomicfile.Opts{})
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sourceGuard); err != nil {
		t.Fatal(err)
	}
	code := <-done
	done = nil
	if code != 0 {
		t.Fatalf("use lost the running session after the older writer stripped its binding: exit %d, %s", code, stderr.String())
	}
	checkLinkTarget(t, link, paths.FableStoreDir(root))
	rec, found, err := reporting.LookupSessionLink(root, placementSession)
	if err != nil || !found || rec.Token != filepath.Base(link) || rec.Family != "opus" || rec.Wanted != "fable" {
		t.Fatalf("repaired link: %+v, found %t, error %v", rec, found, err)
	}
}
