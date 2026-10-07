//go:build !darwin && !windows

package netproxy

func readSystem() settings { return settings{} }
