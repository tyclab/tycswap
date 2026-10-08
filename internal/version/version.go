// Package version uses -ldflags Version, or module build metadata for go install.
// Display removes the leading semver v.
package version

import (
	"runtime/debug"
	"strings"

	"golang.org/x/mod/semver"
)

const unset = "v0.0.0-dev"

var Version = unset

func init() { Version = fromBuildInfo(Version, debug.ReadBuildInfo) }

// A module-cache build takes its module version so update notices do not offer the release already installed.
func fromBuildInfo(v string, read func() (*debug.BuildInfo, bool)) string {
	if v != unset {
		return v
	}
	info, ok := read()
	if !ok || info == nil || !semver.IsValid(info.Main.Version) {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs" || strings.HasPrefix(s.Key, "vcs.") {
			return v
		}
	}
	return info.Main.Version
}

// The form --version prints and the export envelope's swapVersion carries.
func Display() string {
	return strings.TrimPrefix(Version, "v")
}
