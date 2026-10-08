//go:build !linux

package cclock

func renameNoReplace(oldpath, newpath string) error { return renameIfAbsent(oldpath, newpath) }
