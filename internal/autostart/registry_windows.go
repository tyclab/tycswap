//go:build windows

package autostart

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// errInTests is every registry function's answer inside a test binary,
// before it opens the key: a test never reads or changes the user's Run key.
var errInTests = errors.New("autostart: the real HKCU Run key is not reachable from tests; give the test a Config with another GOOS")

func registryEnable(c Config) error {
	if testing.Testing() {
		return errInTests
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(c.Label, registryCommand(c))
}

func registryDisable(c Config) error {
	if testing.Testing() {
		return errInTests
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(c.Label); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func registryEnabled(c Config) (bool, error) {
	if testing.Testing() {
		return false, errInTests
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer k.Close()
	_, _, err = k.GetStringValue(c.Label)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
