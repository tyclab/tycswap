//go:build windows

package update

import "os"

// replaceable reports whether this process may rename a new build over exe:
// on Windows, a regular file that is not read-only. Whether the directory
// takes a new file is its ACL's answer, which cannot be had without writing
// one, so a directory that refuses fails the download, which says so; and
// Windows does not grant a rename by owning the file.
func replaceable(exe string) bool {
	fi, err := os.Stat(exe)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o200 != 0
}
