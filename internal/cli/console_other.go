//go:build !windows

package cli

func releaseOwnConsole(s ioStreams) ioStreams { return s }
