//go:build !windows

// Spec 08§10.2/§10.4 console setup: VT is always available and UTF-8 is native off Windows.

package printer

func isWindows() bool { return false }

func enableWindowsVT() bool { return true }

// ForceUTF8Output is a no-op on non-Windows; Go writes UTF-8 bytes natively.
func ForceUTF8Output() {}
