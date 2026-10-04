//go:build !windows

package cli

// releaseOwnConsole is a Windows concern (console_windows.go, DESIGN A41):
// elsewhere a program started at login has no terminal window to give back.
func releaseOwnConsole(s ioStreams) ioStreams { return s }
