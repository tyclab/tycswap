//go:build linux

package tray

// StatusNotifierItem over D-Bus (sni_linux.go): no session bus or watcher gives ErrUnsupported and `tycswap app` runs headless.
func newTray(icon Icon, opts Options) (Tray, error) {
	return newSNITray(icon, opts)
}
