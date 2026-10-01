//go:build !windows

package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// CheckPrivateRoot refuses a backup root another local user could write: it
// must be owned by the effective user and not writable by group or other.
// The root holds credentials and the lock file; a root someone else can write
// lets them plant symlinks or files tycswap then trusts. An absent root is
// fine (tycswap creates it 0700). A symlinked root is judged by its target.
// Existing modes are never changed here.
func CheckPrivateRoot(root string) error {
	fi, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cannot check the tycswap store %s: %w", root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("the tycswap store %s is not a directory", root)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if euid := os.Geteuid(); int(st.Uid) != euid {
			return fmt.Errorf("refusing to use the tycswap store %s: it is owned by uid %d, not by you (uid %d); "+
				"use a store you own (XDG_DATA_HOME), or fix the owner", root, st.Uid, euid)
		}
	}
	if perm := fi.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("refusing to use the tycswap store %s: it is writable by group or other (mode %04o); "+
			"run: chmod 700 %s", root, perm, root)
	}
	return nil
}
