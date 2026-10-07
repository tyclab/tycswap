package update

import (
	"regexp"
	"runtime/debug"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/tyclab/tycswap/internal/version"
)

type BuildSource int

const (
	// SourceCheckout is a build from a local source tree (or one whose origin
	// cannot be established, which is treated the same way: never reinstall).
	SourceCheckout BuildSource = iota
	SourceModule
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

var describeSuffix = regexp.MustCompile(`-[0-9]+-g[0-9a-f]+(-dirty)?$|-dirty$`)

func releaseTag(v string) bool {
	return semver.IsValid(v) && v != unlinkedVersion && semver.Build(v) == "" &&
		!module.IsPseudoVersion(v) && !describeSuffix.MatchString(v)
}
