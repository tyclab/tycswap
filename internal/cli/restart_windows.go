//go:build windows

package cli

import (
	"os"
)

var restartSelf = func(installedTag string) error {
	exe := exePath()
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return err
		}
	}
	p, err := startDetached(exe, os.Args, append(os.Environ(), justInstalledEnv()+"="+installedTag), appLogPath())
	if err != nil {
		return err
	}
	return p.Release()
}
