//go:build !windows

package autostart

func registryEnable(Config) error          { return ErrUnsupported }
func registryDisable(Config) error         { return ErrUnsupported }
func registryEnabled(Config) (bool, error) { return false, ErrUnsupported }
