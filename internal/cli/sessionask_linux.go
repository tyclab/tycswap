package cli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"golang.org/x/sys/unix"
)

func askerLockPath(link string) string {
	return filepath.Join(filepath.Dir(link), "."+filepath.Base(link)+".ask")
}

// The child inherits the open file description carrying the flock. If either
// process dies before handoff, the next observation can take it immediately.
func startSessionAsker(q sessionQuestion) error {
	lock, err := os.OpenFile(askerLockPath(q.path), unix.O_NOFOLLOW|os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	cmd := exec.Command(exePath(), "_placement-ask", q.id, q.model, q.claim)
	cmd.ExtraFiles = []*os.File{lock}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func sessionAskerCommand(args []string) int {
	if len(args) != 3 || !sessionUUID.MatchString(args[0]) {
		return 2
	}
	root := paths.GetBackupRoot()
	rec, found, err := reporting.LookupSessionLink(root, args[0])
	if err != nil || !found || rec.Question != args[2] || rec.Family != reporting.ClaudeFamily(args[1]) {
		return 1
	}
	link := filepath.Join(root, "links", rec.Token)
	lease := os.NewFile(3, "session allocation question")
	if lease == nil {
		return 1
	}
	defer lease.Close()
	unix.CloseOnExec(3)
	file, err := lease.Stat()
	if err != nil {
		return 1
	}
	entry, err := os.Lstat(askerLockPath(link))
	if err != nil || !entry.Mode().IsRegular() || !os.SameFile(file, entry) || unix.Flock(3, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return 1
	}
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return 1
	}
	result, err := evaluateSessionPlacement(sw, rec.ID, link, args[1], "", false, args[2])
	if err != nil || result.question == nil {
		return 1
	}
	q := *result.question
	broker := os.Getenv("TYCSWAP_PLACEMENT_BROKER")
	var granted bool
	if broker == "" {
		err = errors.New("no placement broker configured")
	} else {
		granted, err = askPlacementBroker(broker, q.text, q.id)
	}
	if err := answerSessionQuestion(sw, q, granted, err); err != nil {
		logging.New(root, false).Debugf("Session placement: %v", err)
		return 1
	}
	return 0
}
