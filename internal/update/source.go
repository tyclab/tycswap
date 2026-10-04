// Build-source detection: was the running binary built by `go install
// <module>@<version>` from the module proxy, or from a local checkout
// (`make build`, `make install`, `go build`, `go install ./cmd/tycswap`)?
// Only the first may be upgraded by re-running `go install <ModulePath>@latest`;
// doing that to a checkout build would silently replace the user's own tree
// with whatever the remote publishes (Amendment A24).
package update

import (
	"runtime/debug"
	"strings"

	"golang.org/x/mod/semver"
)

// BuildSource classifies where the running binary came from.
type BuildSource int

const (
	// SourceCheckout is a build from a local source tree (or one whose origin
	// cannot be established, which is treated the same way: never reinstall).
	SourceCheckout BuildSource = iota
	// SourceModule is a `go install <module>@<version>` build from the module
	// cache: a real module version and no VCS stamp.
	SourceModule
	// SourceRelease is a build of the release workflow (DESIGN A35): no VCS
	// stamp (-buildvcs=false), -trimpath, and a release version linked into
	// internal/version. `tycswap upgrade` downloads the next release over it.
	SourceRelease
)

// versionVar is the variable the release workflow sets with -X.
const versionVar = "github.com/tyclab/tycswap/internal/version.Version="

// readBuildInfo is the build-info seam, swapped by tests.
var readBuildInfo = debug.ReadBuildInfo

// DetectBuildSource reads the running binary's build info. A checkout build
// carries "vcs.*" settings (Go stamps them when building inside a repository)
// or the "(devel)" main-module version (when -buildvcs=false); a module-cache
// build carries neither and has a real version for the main module.
func DetectBuildSource() BuildSource {
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return SourceCheckout
	}
	return classifyBuildInfo(info)
}

func classifyBuildInfo(info *debug.BuildInfo) BuildSource {
	var trimpath bool
	var ldflags string
	for _, s := range info.Settings {
		switch {
		case s.Key == "vcs" || len(s.Key) > 4 && s.Key[:4] == "vcs.":
			return SourceCheckout
		case s.Key == "-trimpath":
			trimpath = s.Value == "true"
		case s.Key == "-ldflags":
			ldflags = s.Value
		}
	}
	switch info.Main.Version {
	case "", "(devel)":
		if trimpath && releaseVersion(ldflags) {
			return SourceRelease
		}
		return SourceCheckout
	}
	return SourceModule
}

// releaseVersion reports whether ldflags link a release version (a valid
// semver tag) into internal/version.
func releaseVersion(ldflags string) bool {
	for _, f := range strings.Fields(ldflags) {
		if v, ok := strings.CutPrefix(f, versionVar); ok && semver.IsValid(v) && semver.Prerelease(v) == "" {
			return true
		}
	}
	return false
}
