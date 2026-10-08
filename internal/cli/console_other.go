//go:build !windows

package cli

// Windows-only concern (console_windows.go, DESIGN A41): elsewhere a login-started program has no console to give back.
func releaseOwnConsole(s ioStreams) ioStreams { return s }
