//go:build !windows

package cli

// hasConsole: a console window is a Windows notion.
func hasConsole() bool { return false }

func consoleCountForTest() int { return 0 }
