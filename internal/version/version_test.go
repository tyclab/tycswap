package version

import (
	"runtime/debug"
	"testing"
)

func TestDefaultVersion(t *testing.T) {
	// Default (un-injected) build marker per DESIGN A5.
	if Version != "v0.0.0-dev" {
		t.Errorf("default Version = %q, want v0.0.0-dev", Version)
	}
}

func TestDisplayStripsLeadingV(t *testing.T) {
	cases := map[string]string{
		"v0.1.0":        "0.1.0",
		"v0.2.0-beta.1": "0.2.0-beta.1",
		"v0.0.0-dev":    "0.0.0-dev",
		"0.1.0":         "0.1.0", // no leading v → unchanged
	}
	saved := Version
	defer func() { Version = saved }()
	for in, want := range cases {
		Version = in
		if got := Display(); got != want {
			t.Errorf("Display() with Version=%q = %q, want %q", in, got, want)
		}
	}
}

// A `go install <module>@<version>` build carries no -ldflags: its version
// comes from the build info, so the update check does not offer the release
// that is already installed. Anything injected, and any checkout build, is
// left alone.
func TestFromBuildInfo(t *testing.T) {
	module := &debug.BuildInfo{Main: debug.Module{Path: "github.com/tyclab/tycswap", Version: "v0.4.0"}}
	checkout := &debug.BuildInfo{Main: debug.Module{Version: "v0.4.1-0.20261002120000-110a814d619d+dirty"},
		Settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "110a814"}}}
	read := func(info *debug.BuildInfo, ok bool) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return info, ok }
	}
	cases := []struct {
		name string
		v    string
		read func() (*debug.BuildInfo, bool)
		want string
	}{
		{"module build", unset, read(module, true), "v0.4.0"},
		{"injected wins", "v0.3.0", read(module, true), "v0.3.0"},
		{"checkout build", unset, read(checkout, true), unset},
		{"devel", unset, read(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true), unset},
		{"no build info", unset, read(nil, false), unset},
	}
	for _, c := range cases {
		if got := fromBuildInfo(c.v, c.read); got != c.want {
			t.Errorf("%s: fromBuildInfo = %q, want %q", c.name, got, c.want)
		}
	}
}
