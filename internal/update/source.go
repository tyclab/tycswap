package update

import (
	"regexp"
	"runtime/debug"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/tyclab/tycswap/internal/version"
)

// Only a module-proxy build may be re-installed with go install @latest: on a checkout it would replace the user's tree (A24).
type BuildSource int

const (
	// SourceCheckout is a build from a local source tree (or one whose origin
	// cannot be established, which is treated the same way: never reinstall).
	SourceCheckout BuildSource = iota
	SourceModule
	// A release-workflow build: no VCS stamp, no module version, a release tag linked into internal/version (DESIGN A35).
	SourceRelease
)

var readBuildInfo = debug.ReadBuildInfo

var linkedVersion = func() string { return version.Version }

func DetectBuildSource() BuildSource {
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return SourceCheckout
	}
	return classifyBuildInfo(info, linkedVersion())
}

// Build flags are not read: -trimpath drops -ldflags from build info (go.dev/issue/52372); since Go 1.24 a VCS build also
// carries a pseudo-version. Order: VCS stamp → checkout; real semver → module; no version + release tag → release; else checkout.
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

const unlinkedVersion = "v0.0.0-dev"

// What `git describe` appends between tags or on a dirty tree; the Makefile links that.
var describeSuffix = regexp.MustCompile(`-[0-9]+-g[0-9a-f]+(-dirty)?$|-dirty$`)

func releaseTag(v string) bool {
	return semver.IsValid(v) && v != unlinkedVersion && semver.Build(v) == "" &&
		!module.IsPseudoVersion(v) && !describeSuffix.MatchString(v)
}
