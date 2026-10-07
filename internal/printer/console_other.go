//go:build !windows

package printer

func isWindows() bool { return false }

func enableWindowsVT() bool { return true }

// ForceUTF8Output is a no-op on non-Windows; Go writes UTF-8 bytes natively.
func ForceUTF8Output() {}
