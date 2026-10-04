// The update-notice hint clause, keyed by the upgrade plan and platform.
//
// Implements the hint half of spec 08§13.2 (check_for_update), redesigned per
// Amendments A6 and A36 to key off the upgrade plan instead of Python's
// uv/pipx _detect_install_method.
package update

import "github.com/tyclab/tycswap/internal/platform"

// UpgradeHint returns the actionable clause appended to an update notice,
// from the plan SelfUpgrade would follow (UpgradePlan).
//
// Python's check_for_update picks among three hints keyed by install method
// (uv/pipx/unknown) and platform (spec 08§13.2); the Go redesign keys them
// off the plan instead (Amendments A6, A36): a binary `tycswap upgrade`
// upgrades itself gets "tycswap upgrade does it" (a go-installed one on
// Windows the literal command: SelfUpgrade there is print-only — the running
// .exe is locked), a checkout build CheckoutHint, a package manager's binary
// the package manager, and any other the generic "see instructions" hint.
func UpgradeHint(p Plan, plat platform.Platform) string {
	switch {
	case p.Method == MethodCheckout:
		return "This binary was " + CheckoutHint + "."
	case p.Method == MethodPackageManager:
		return "It was installed by a package manager: update it with " + p.Updater() + "."
	case p.Method == MethodGoInstall && plat == platform.Windows:
		return "Run `go install " + ModulePath + "@latest` to update."
	case p.Method == MethodGoInstall, p.Method == MethodDownload:
		return "Run `tycswap upgrade` to update."
	default:
		return "Run `tycswap upgrade` for upgrade instructions."
	}
}
