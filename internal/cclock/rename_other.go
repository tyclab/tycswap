//go:build !linux

package cclock

// renameNoReplace is the checked rename where no atomic no-replace rename is
// available: a lock taken between the check and the rename can be replaced.
func renameNoReplace(oldpath, newpath string) error { return renameIfAbsent(oldpath, newpath) }
