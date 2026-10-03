// Package version holds the build-time-injected program version.
//
// Implements DESIGN A5 / audit 10 Gap 1. There is no importlib.metadata in Go:
// the version is embedded at build time via
//
//	-ldflags "-X github.com/tyclab/tycswap/internal/version.Version=v0.1.0"
//
// A `go install <module>@<version>` build gets no -ldflags; it takes the
// module version its build info records instead (fromBuildInfo).
//
// The port's own versions are semver with a leading v; Display strips the v for
// parity with Python's --version / swapVersion presentation.
package version

import (
	"runtime/debug"
	"strings"

	"golang.org/x/mod/semver"
)

// unset is Version when nothing was injected at link time.
const unset = "v0.0.0-dev"

// Version is the semver build version (leading v). Overridden at link time; the
// default marks an un-injected local build.
var Version = unset

func init() { Version = fromBuildInfo(Version, debug.ReadBuildInfo) }

// fromBuildInfo is v, unless nothing was injected and the binary is a
// module-cache build (`go install <module>@<version>`: a real module version
// and no VCS stamp): then the module version, so the update notice and the
// dashboard's Updates card do not offer the release already installed. A
// checkout build (VCS stamps or "(devel)") keeps the default.
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

// Display returns Version without its leading v, the form shown by --version and
// carried in the export envelope's swapVersion.
func Display() string {
	return strings.TrimPrefix(Version, "v")
}
