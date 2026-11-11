//go:build !windows

package autostart

func execSchtasks(...string) ([]byte, uint32, error) { return nil, 0, ErrUnsupported }
