package reporting

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"golang.org/x/sys/unix"
)

func TestUseTakesClientLockOnlyAfterBothStoreLocks(t *testing.T) {
	for _, name := range []string{".lock", ".default_models.lock"} {
		t.Run(name, func(t *testing.T) {
			root, link, source, dest := linkFixture(t)
			path := filepath.Join(root, name)
			held := filelock.New(path, time.Second)
			if ok, err := held.Acquire(time.Second); !ok || err != nil {
				t.Fatalf("holding %s: %v", name, err)
			}
			defer held.Release()
			// filelock opens its target before trying flock. Watch that open so
			// the assertion runs when use has actually reached this lock.
			fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if _, err := unix.InotifyAddWatch(fd, path, unix.IN_OPEN); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- UseSessionLink(root, linkSessionID, link, time.Now(), func(rec *SessionLink) error {
					rec.Target = dest
					return nil
				})
			}()
			defer func() {
				held.Release()
				if err := <-done; err != nil {
					t.Errorf("use did not finish the move: %v", err)
				}
				if target, err := os.Readlink(link); err != nil || target != dest {
					t.Errorf("after releasing %s: target %q, %v", name, target, err)
				}
			}()
			if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 2000); n != 1 || err != nil {
				t.Fatalf("use never reached %s: %d, %v", name, n, err)
			}
			refresh := filepath.Join(source, ".oauth_refresh.lock")
			if err := os.Mkdir(refresh, 0700); err != nil {
				t.Fatalf("client cannot refresh while use waits for %s: %v", name, err)
			}
			if err := os.Remove(refresh); err != nil {
				t.Fatal(err)
			}
		})
	}
}
