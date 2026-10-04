//go:build linux

package tray

// Linux: the StatusNotifierItem protocol over D-Bus, spoken directly (see
// sni_linux.go). newTray fails with ErrUnsupported when there is no session
// bus or no StatusNotifierWatcher (a bare console session, most CI boxes), and
// `tycswap app` then runs headless.
func newTray(icon Icon, opts Options) (Tray, error) {
	return newSNITray(icon, opts)
}
