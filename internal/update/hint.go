package update

import "github.com/tyclab/tycswap/internal/platform"

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
