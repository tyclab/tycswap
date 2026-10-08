//go:build windows

package update

import "os"

// The directory ACL cannot be checked without writing a file, so a refusing directory fails the download instead.
// Owning the file does not grant a rename on Windows, so only regular and not read-only is checked.
func replaceable(exe string) bool {
	fi, err := os.Stat(exe)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o200 != 0
}
