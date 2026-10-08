//go:build windows

package update

import "os"

func replaceable(exe string) bool {
	fi, err := os.Stat(exe)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o200 != 0
}
