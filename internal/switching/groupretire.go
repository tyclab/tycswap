package switching

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/store"
)

// RetireGroup ends a session group (DESIGN A78) once no session runs in its
// profile: its login goes back to the account's slot through the verified
// release (A69), its conversations are copied into the default profile where
// missing, and its control files go. The profile stays, with its history and
// MCP sign-ins. Each step can rerun, so an interrupted retirement finishes on
// a later tick; ErrGroupBusy means a session still runs there.
func RetireGroup(s *store.Store) (*groups.Account, error) {
	released, err := releaseGroup(s)
	if err != nil {
		return nil, err
	}
	if err := copyHistory(filepath.Join(s.ProfileDir(), "projects"), filepath.Join(s.DefaultProfileDir(), "projects")); err != nil {
		return released, err
	}
	return released, groups.Retire(s.SharedRoot(), s.GroupID())
}

// copyHistory copies every conversation file the destination lacks; nothing
// in the destination is replaced.
func copyHistory(from, to string) error {
	if err := groups.SafePath(from); err != nil {
		return err
	}
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(to, rel)
		if _, err := os.Lstat(dest); err == nil || !os.IsNotExist(err) {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		return atomicfile.Write(dest, data, atomicfile.Opts{})
	})
}
