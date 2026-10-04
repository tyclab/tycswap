//go:build (darwin && !cgo) || (!darwin && !windows && !linux)

package tray

func newTray(Icon, Options) (Tray, error) { return nil, ErrUnsupported }
