package store

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/procdetect"
)

// FableReaders identifies consumers of an automatically recovered login. A
// Unix native launch replaces its launcher PID; on Windows the launcher
// waits for the child. A reused PID conservatively keeps the login in place.
type FableReaders struct {
	Identity string `json:"identity"`
	PIDs     []int  `json:"pids"`
}

func (r FableReaders) InUse() bool { return slices.ContainsFunc(r.PIDs, procdetect.IsPIDAlive) }

func readerPath(root string) string {
	return filepath.Join(paths.FableScopeDir(root), "recovered-readers.json")
}

func ReadFableReaders(root string) (FableReaders, error) {
	var r FableReaders
	raw, err := os.ReadFile(readerPath(root))
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	hash, err := hex.DecodeString(r.Identity)
	if err != nil || len(hash) != 32 || len(r.PIDs) == 0 || slices.ContainsFunc(r.PIDs, func(pid int) bool { return pid <= 1 }) {
		return r, errors.New("invalid recovered Fable session record")
	}
	return r, nil
}

// PinFableReader is called under the native store and credential locks, before
// a new process can receive the selected identity.
func (s *Store) PinFableReader(identity string) error {
	r, err := ReadFableReaders(s.BackupDir())
	if err != nil {
		return err
	}
	if r.Identity != "" && r.Identity != identity {
		return errors.New("the recovered Fable login identity changed")
	}
	r.Identity = identity
	r.PIDs = slices.DeleteFunc(r.PIDs, func(pid int) bool { return !procdetect.IsPIDAlive(pid) })
	if !slices.Contains(r.PIDs, os.Getpid()) {
		r.PIDs = append(r.PIDs, os.Getpid())
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicfile.Write(readerPath(s.BackupDir()), raw, atomicfile.Opts{})
}

// ReleaseIdleFableReaders precedes every shared identity change or removal,
// under the native write lock. Unknown registered clients also keep the login.
func (s *Store) ReleaseIdleFableReaders() error {
	r, err := ReadFableReaders(s.BackupDir())
	if err != nil || r.Identity == "" {
		return err
	}
	if r.InUse() {
		return errors.New("the recovered Fable login is in use")
	}
	if err := s.CheckFableIdle(); err != nil {
		return err
	}
	return os.Remove(readerPath(s.BackupDir()))
}

func (s *Store) CheckFableIdle() error {
	pids, err := LiveProfilePIDs(s.DefaultProfileDir())
	if err != nil {
		return err
	}
	held, err := autoswitch.StartHeld(s.StatePath(), s.Clk.Now())
	if err != nil {
		return err
	}
	if len(pids) != 0 || held {
		return errors.New("a Claude process or launch may still use the recovered Fable login")
	}
	return nil
}
