//go:build !linux

package cclock

// No atomic no-replace rename here: a lock taken between the check and the rename can be replaced.
func renameNoReplace(oldpath, newpath string) error { return renameIfAbsent(oldpath, newpath) }
