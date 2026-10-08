//go:build !darwin && !windows

package netproxy

// No system proxy setting to read: on Linux the environment is the convention.
func readSystem() settings { return settings{} }
