package switching

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"

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
	// A session started by hand since the release would race the copy.
	if pids, err := store.LiveProfilePIDs(s.ProfileDir()); err != nil || len(pids) > 0 {
		return released, ErrGroupBusy
	}
	if err := copyHistory(filepath.Join(s.ProfileDir(), "projects"), filepath.Join(s.DefaultProfileDir(), "projects")); err != nil {
		return released, err
	}
	return released, groups.Retire(s.SharedRoot(), s.GroupID())
}

// copyHistory copies every conversation file the destination lacks, streamed
// through a temporary file; nothing in the destination is replaced, and
// symlinks in the group's profile are not followed.
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
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		return copyFile(path, dest)
	})
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(to), ".tycswap-copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), to)
}
