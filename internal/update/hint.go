// The update-notice hint clause, keyed by install shape and platform.
//
// Implements the hint half of spec 08§13.2 (check_for_update), redesigned per
// Amendment A6 to key off InstallShape instead of Python's uv/pipx
// _detect_install_method.
package update

import "github.com/tyclab/tycswap/internal/platform"

// UpgradeHint returns the actionable clause appended to an update notice.
//
// Python's check_for_update picks among three hints keyed by install method
// (uv/pipx/unknown) and platform (spec 08§13.2); the Go redesign has no
// uv/pipx equivalent, so the same three-way shape is reproduced against
// InstallShape instead (Amendment A6): a go-install shape on a
// self-upgrade-capable platform gets the "tycswap upgrade does it" hint, the
// same shape on Windows gets the literal command (SelfUpgrade there is
// print-only — the running .exe is locked), and an unknown shape gets the
// generic "see instructions" hint.
//
// A binary built from a checkout gets CheckoutHint whatever its shape: `make
// install` puts it in a Go bin dir, but upgrading it is the checkout's job.
func UpgradeHint(shape InstallShape, plat platform.Platform) string {
	if DetectBuildSource() == SourceCheckout {
		return "This binary was " + CheckoutHint + "."
	}
	switch {
	case shape == ShapeGoInstall && plat != platform.Windows:
		return "Run `tycswap upgrade` to update."
	case shape == ShapeGoInstall:
		return "Run `go install " + ModulePath + "@latest` to update."
	default:
		return "Run `tycswap upgrade` for upgrade instructions."
	}
}
