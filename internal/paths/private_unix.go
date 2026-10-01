//go:build !windows

package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// PrivateRootStat and PrivateRootChmod are the calls CheckPrivateRoot makes.
// Tests replace them to stand in for a filesystem whose modes chmod cannot
// change.
var (
	PrivateRootStat  = os.Stat
	PrivateRootChmod = os.Chmod
)

// CheckPrivateRoot refuses a backup root another local user owns, and makes
// one that is writable by group or other private. The root holds credentials
// and the lock file; a root someone else can write lets them plant symlinks
// or files tycswap then trusts. An absent root is fine (tycswap creates it
// 0700). A symlinked root is judged by its target.
//
// A root writable by group or other is chmod 0700 and checked again. When
// its mode did not change, as on a filesystem that ignores chmod (a Windows
// drive under WSL reports 0777 for the user's own directories), the root is
// used and the returned warning says so in one line; "" means nothing to
// say.
func CheckPrivateRoot(root string) (warning string, err error) {
	fi, err := PrivateRootStat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("cannot check the tycswap store %s: %w", root, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("the tycswap store %s is not a directory", root)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if euid := os.Geteuid(); int(st.Uid) != euid {
			return "", fmt.Errorf("refusing to use the tycswap store %s: it is owned by uid %d, not by you (uid %d); "+
				"use a store you own (XDG_DATA_HOME), or fix the owner", root, st.Uid, euid)
		}
	}
	perm := fi.Mode().Perm()
	if perm&0o022 == 0 {
		return "", nil
	}
	// The chmod's own result does not matter: only the mode it leaves does.
	_ = PrivateRootChmod(root, 0o700)
	fi, err = PrivateRootStat(root)
	if err != nil {
		return "", fmt.Errorf("cannot check the tycswap store %s: %w", root, err)
	}
	if fi.Mode().Perm()&0o022 == 0 {
		return "", nil
	}
	return fmt.Sprintf("the tycswap store %s is writable by group or other (mode %04o) and chmod 700 did not change that; "+
		"a filesystem without POSIX modes (a Windows drive under WSL) reports this for your own directories, "+
		"otherwise run: chmod 700 %s", root, perm, shellQuote(root)), nil
}

// shellQuote single-quotes s for a command the user pastes into a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
