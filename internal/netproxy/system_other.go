//go:build !darwin && !windows

package netproxy

// readSystem has no system setting to read: on Linux the environment is the
// convention.
func readSystem() settings { return settings{} }
