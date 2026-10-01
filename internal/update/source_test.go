package update

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/platform"
)

// moduleBuild is what `go install github.com/tyclab/tycswap/cmd/tycswap@v1.2.3`
// stamps: a real version, no VCS settings.
func moduleBuild() (*debug.BuildInfo, bool) {
	return &debug.BuildInfo{
		Path: "github.com/tyclab/tycswap/cmd/tycswap",
		Main: debug.Module{Path: "github.com/tyclab/tycswap", Version: "v1.2.3"},
	}, true
}

// checkoutBuild is what `make build` / `make install` in a clone stamps.
func checkoutBuild() (*debug.BuildInfo, bool) {
	return &debug.BuildInfo{
		Path: "github.com/tyclab/tycswap/cmd/tycswap",
		Main: debug.Module{Path: "github.com/tyclab/tycswap", Version: "v0.0.0-20261001120000-abcdef123456+dirty"},
		Settings: []debug.BuildSetting{
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: "abcdef123456"},
		},
	}, true
}

// TestMain runs the package's tests as a module-installed binary: the test
// binary itself is a checkout build, which would short-circuit every
// go-install path. Tests that need the checkout shape swap it back.
func TestMain(m *testing.M) {
	readBuildInfo = moduleBuild
	os.Exit(m.Run())
}

func withBuild(t *testing.T, f func() (*debug.BuildInfo, bool)) {
	t.Helper()
	prev := readBuildInfo
	readBuildInfo = f
	t.Cleanup(func() { readBuildInfo = prev })
}

func TestClassifyBuildInfo(t *testing.T) {
	cases := []struct {
		name string
		info func() (*debug.BuildInfo, bool)
		want BuildSource
	}{
		{"module cache", moduleBuild, SourceModule},
		{"checkout with vcs stamp", checkoutBuild, SourceCheckout},
		{"devel, -buildvcs=false", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
		}, SourceCheckout},
		{"no build info", func() (*debug.BuildInfo, bool) { return nil, false }, SourceCheckout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withBuild(t, tc.info)
			if got := DetectBuildSource(); got != tc.want {
				t.Errorf("DetectBuildSource = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelfUpgrade_GoInstallShapeInstallsTycswap(t *testing.T) {
	var name string
	var args []string
	u, exe, _, _ := goInstallUpgrader(t, fakeRunner(&name, &args, 0, nil))
	if code := u.SelfUpgrade(exe, platform.Linux); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	want := []string{"install", "github.com/tyclab/tycswap/cmd/tycswap@latest"}
	if name != "go" || strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("ran %s %v, want go %v", name, args, want)
	}
}

func TestSelfUpgrade_CheckoutPrintsHintInstallsNothing(t *testing.T) {
	withBuild(t, checkoutBuild)
	for _, plat := range []platform.Platform{platform.Linux, platform.Windows, platform.MacOS} {
		var name string
		var args []string
		u, exe, stdout, stderr := goInstallUpgrader(t, fakeRunner(&name, &args, 0, nil))
		code := u.SelfUpgrade(exe, plat)
		if name != "" {
			t.Errorf("%v: ran %s %v; a checkout build must install nothing", plat, name, args)
		}
		if code != 1 || !strings.Contains(stdout.String(), "built from a checkout: git pull && make install") {
			t.Errorf("%v: exit %d, stdout %q", plat, code, stdout.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), "go install") {
			t.Errorf("%v: checkout guidance mentions go install: %q", plat, stdout.String()+stderr.String())
		}
	}
}

func TestNoticeNamesTyclabReleases(t *testing.T) {
	if Endpoint != "https://api.github.com/repos/tyclab/tycswap/releases/latest" {
		t.Errorf("Endpoint = %q, want tyclab/tycswap releases", Endpoint)
	}
	if ReleasesURL != "https://github.com/tyclab/tycswap/releases" {
		t.Errorf("ReleasesURL = %q", ReleasesURL)
	}
	if ModulePath != "github.com/tyclab/tycswap/cmd/tycswap" {
		t.Errorf("ModulePath = %q", ModulePath)
	}

	var gotPath, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept = r.URL.Path, r.Header.Get("Accept")
		io.WriteString(w, `{"tag_name":"v9.0.0"}`)
	}))
	t.Cleanup(srv.Close)
	withEndpoint(t, srv.URL+"/repos/tyclab/tycswap/releases/latest")
	msg := Checker{CacheDir: t.TempDir(), Clk: fakeAt(1000)}.CheckForUpdate("", "v0.1.0", platform.Linux)
	if gotPath != "/repos/tyclab/tycswap/releases/latest" || gotAccept != "application/vnd.github+json" {
		t.Errorf("request %q (Accept %q)", gotPath, gotAccept)
	}
	if !strings.Contains(msg, "A newer version of tycswap is available (9.0.0)") {
		t.Errorf("notice = %q", msg)
	}

	withBuild(t, checkoutBuild)
	msg = Checker{CacheDir: t.TempDir(), Clk: fakeAt(1000)}.CheckForUpdate("", "v0.1.0", platform.Linux)
	if !strings.HasSuffix(msg, "This binary was built from a checkout: git pull && make install.") {
		t.Errorf("checkout notice = %q", msg)
	}
}

// TestLdflagsOverride links a tiny program with -X on all three variables and
// checks it prints the overrides: proof they are link-time settable vars.
func TestLdflagsOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	const pkg = "github.com/tyclab/tycswap/internal/update"
	flags := strings.Join([]string{
		"-X " + pkg + ".ModulePath=example.com/fork/cmd/tycswap",
		"-X " + pkg + ".Endpoint=https://example.com/api/releases/latest",
		"-X " + pkg + ".ReleasesURL=https://example.com/releases",
	}, " ")
	out := filepath.Join(t.TempDir(), "printvars")
	build := exec.CommandContext(context.Background(), gobin, "build", "-ldflags", flags, "-o", out, "./testdata/printvars")
	var stderr bytes.Buffer
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build: %v\n%s", err, stderr.String())
	}
	got, err := exec.Command(out).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "example.com/fork/cmd/tycswap\nhttps://example.com/api/releases/latest\nhttps://example.com/releases\n"
	if string(got) != want {
		t.Errorf("printvars = %q, want %q", got, want)
	}
}
