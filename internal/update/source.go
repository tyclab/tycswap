// Build-source detection: was the running binary built by `go install
// <module>@<version>` from the module proxy, or from a local checkout
// (`make build`, `make install`, `go build`, `go install ./cmd/tycswap`)?
// Only the first may be upgraded by re-running `go install <ModulePath>@latest`;
// doing that to a checkout build would silently replace the user's own tree
// with whatever the remote publishes (Amendment A24).
package update

import (
	"regexp"
	"runtime/debug"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/tyclab/tycswap/internal/version"
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
	// stamp, no module version, and a release tag linked into
	// internal/version. `tycswap upgrade` downloads the next release over it.
	SourceRelease
)

// readBuildInfo is the build-info seam, swapped by tests.
var readBuildInfo = debug.ReadBuildInfo

// linkedVersion is the version linked into internal/version, the seam tests
// swap.
var linkedVersion = func() string { return version.Version }

// DetectBuildSource classifies the running binary (classifyBuildInfo).
func DetectBuildSource() BuildSource {
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return SourceCheckout
	}
	return classifyBuildInfo(info, linkedVersion())
}

// classifyBuildInfo is DESIGN A36's decision table, on what a build records
// for certain. Build flags are not read: Go records no -buildvcs, and it
// leaves -ldflags out of the build info whenever -trimpath is set
// (go.dev/issue/52372), which is how every release build is made.
//
//  1. A VCS stamp (any vcs.* setting): a checkout build. Since Go 1.24 such a
//     build also carries a pseudo-version as its module version.
//  2. A module version that is valid semver and not a pseudo-version:
//     `go install <module>@<version>`.
//  3. No module version ("" or "(devel)") and a release tag linked into
//     internal/version (releaseTag): a release build.
//  4. Anything else: a checkout build.
func classifyBuildInfo(info *debug.BuildInfo, linked string) BuildSource {
	for _, s := range info.Settings {
		if s.Key == "vcs" || strings.HasPrefix(s.Key, "vcs.") {
			return SourceCheckout
		}
	}
	v := info.Main.Version
	switch {
	case semver.IsValid(v) && !module.IsPseudoVersion(v):
		return SourceModule
	case (v == "" || v == "(devel)") && releaseTag(linked):
		return SourceRelease
	}
	return SourceCheckout
}

// unlinkedVersion is internal/version's Version when nothing was linked.
const unlinkedVersion = "v0.0.0-dev"

// describeSuffix is what `git describe` puts after a tag: -N-gHASH between
// tags, -dirty for a modified tree (the Makefile links that).
var describeSuffix = regexp.MustCompile(`-[0-9]+-g[0-9a-f]+(-dirty)?$|-dirty$`)

// releaseTag reports whether v is a tag the release workflow links: vX.Y.Z,
// or a pre-release tag such as vX.Y.Z-rc.1. Not the unlinked v0.0.0-dev, a
// pseudo-version, build metadata or a `git describe` between tags.
func releaseTag(v string) bool {
	return semver.IsValid(v) && v != unlinkedVersion && semver.Build(v) == "" &&
		!module.IsPseudoVersion(v) && !describeSuffix.MatchString(v)
}
