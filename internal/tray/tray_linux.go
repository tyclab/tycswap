//go:build linux

package tray

func newTray(icon Icon, opts Options) (Tray, error) {
	return newSNITray(icon, opts)
}
