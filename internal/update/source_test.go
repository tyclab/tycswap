package update

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

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

// withLinkedVersion makes v the version linked into internal/version.
func withLinkedVersion(t *testing.T, v string) {
	t.Helper()
	prev := linkedVersion
	linkedVersion = func() string { return v }
	t.Cleanup(func() { linkedVersion = prev })
}

// DESIGN A36's decision table: a VCS stamp, then the module version, then
// the version linked into internal/version. Build flags play no part.
func TestClassifyBuildInfo(t *testing.T) {
	build := func(version string, settings ...debug.BuildSetting) *debug.BuildInfo {
		return &debug.BuildInfo{
			Path:     "github.com/tyclab/tycswap/cmd/tycswap",
			Main:     debug.Module{Path: "github.com/tyclab/tycswap", Version: version},
			Settings: settings,
		}
	}
	vcs := []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "abcdef123456"}, {Key: "vcs.modified", Value: "false"}}
	trimpath := debug.BuildSetting{Key: "-trimpath", Value: "true"}
	cases := []struct {
		name   string
		info   *debug.BuildInfo
		linked string
		want   BuildSource
	}{
		{"go install m@v1.2.3", build("v1.2.3"), "v1.2.3", SourceModule},
		{"a module version with build metadata", build("v1.2.3+incompatible"), unlinkedVersion, SourceModule},
		{"checkout with a vcs stamp", build("(devel)", vcs...), unlinkedVersion, SourceCheckout},
		{"Go 1.26 checkout: pseudo-version and vcs stamp", build("v0.6.1-0.20261004120000-abcdef123456+dirty", vcs...), unlinkedVersion, SourceCheckout},
		{"a vcs stamp beats a release tag", build("(devel)", append(vcs, trimpath)...), "v0.6.0", SourceCheckout},
		{"pseudo-version without a stamp", build("v0.0.0-20261001120000-abcdef123456"), unlinkedVersion, SourceCheckout},
		{"-buildvcs=false, nothing linked", build("(devel)"), unlinkedVersion, SourceCheckout},
		{"the release workflow", build("(devel)", trimpath), "v0.6.0", SourceRelease},
		{"a release tag without -trimpath", build("(devel)"), "v0.6.0", SourceRelease},
		{"no module version at all", build(""), "v0.6.0", SourceRelease},
		{"a release candidate", build("(devel)", trimpath), "v0.7.0-rc.1", SourceRelease},
		{"git describe between tags", build("(devel)", trimpath), "v0.6.0-3-gabcdef1", SourceCheckout},
		{"git describe, dirty", build("(devel)"), "v0.6.0-3-gabcdef1-dirty", SourceCheckout},
		{"a dirty tag", build("(devel)"), "v0.6.0-dirty", SourceCheckout},
		{"a pseudo-version linked", build("(devel)"), "v0.0.0-20261001120000-abcdef123456", SourceCheckout},
		{"a bare hash linked", build("(devel)"), "abcdef1", SourceCheckout},
		{"not semver", build("(devel)"), "0.6.0", SourceCheckout},
	}
	for _, tc := range cases {
		if got := classifyBuildInfo(tc.info, tc.linked); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// DetectBuildSource reads the seams; no build info is a checkout build.
	withBuild(t, releaseBuild)
	withLinkedVersion(t, "v0.6.0")
	if got := DetectBuildSource(); got != SourceRelease {
		t.Errorf("DetectBuildSource = %v, want release", got)
	}
	withBuild(t, func() (*debug.BuildInfo, bool) { return nil, false })
	if got := DetectBuildSource(); got != SourceCheckout {
		t.Errorf("no build info: %v, want checkout", got)
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
	if runtime.GOOS == "windows" {
		out += ".exe" // go build -o keeps the name; exec finds only .exe
	}
	build := exec.CommandContext(context.Background(), gobin, "build", "-ldflags", flags, "-o", out, "./testdata/printvars")
	build.Env = append(os.Environ(), noGitConfig...)
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

// TestClassifyRealBuilds builds tycswap the ways it is built and classifies
// what each binary's build info really records: the release workflow's flags
// (a release: the info has no VCS stamp and no module version, and the -X
// that links the tag is not in it), a plain `go build` in the checkout (a
// checkout build), and `go install <module>@<version>` from a module proxy —
// a directory holding this checkout as v0.6.0, the dependencies coming from
// the local module cache (a module build). A fake build info cannot stand in
// here: what a real build records is the point.
func TestClassifyRealBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gocmd := func(wd string, env []string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), gobin, args...)
		cmd.Dir = wd
		cmd.Env = append(append(os.Environ(), noGitConfig...), env...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	classify := func(bin, linked string) BuildSource {
		t.Helper()
		info, err := buildinfo.ReadFile(bin)
		if err != nil {
			t.Fatalf("%s: %v", bin, err)
		}
		return classifyBuildInfo(info, linked)
	}

	// .github/workflows/release.yml, for linux and windows.
	release := filepath.Join(dir, "release")
	gocmd(root, []string{"CGO_ENABLED=0"}, "build", "-trimpath", "-buildvcs=false",
		"-ldflags", "-s -w -X github.com/tyclab/tycswap/internal/version.Version=v0.6.0",
		"-o", release, "./cmd/tycswap")
	if got := classify(release, "v0.6.0"); got != SourceRelease {
		t.Errorf("release workflow build: %v, want release", got)
	}

	plain := filepath.Join(dir, "plain")
	gocmd(root, nil, "build", "-o", plain, "./cmd/tycswap")
	if got := classify(plain, unlinkedVersion); got != SourceCheckout {
		t.Errorf("plain go build: %v, want checkout", got)
	}

	const modPath, modVersion = "github.com/tyclab/tycswap", "v0.6.0"
	at := filepath.Join(dir, "proxy", filepath.FromSlash(modPath), "@v")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"list":               []byte(modVersion + "\n"),
		modVersion + ".info": []byte(`{"Version":"` + modVersion + `"}`),
		modVersion + ".mod":  gomod,
	} {
		if err := os.WriteFile(filepath.Join(at, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	zf, err := os.Create(filepath.Join(at, modVersion+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(zf, module.Version{Path: modPath, Version: modVersion}, root); err != nil {
		zf.Close()
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	modcache := gocmd(root, nil, "env", "GOMODCACHE")
	installed := filepath.Join(dir, "gobin")
	gocmd(dir, []string{
		"GOENV=off", "GOWORK=off", "GOFLAGS=-modcacherw", "GOTOOLCHAIN=local", "GOSUMDB=off",
		"GOPROXY=" + fileURL(filepath.Join(dir, "proxy")) + "," + fileURL(filepath.Join(modcache, "cache", "download")),
		"GOMODCACHE=" + filepath.Join(dir, "modcache"), "GOBIN=" + installed,
	}, "install", modPath+"/internal/update/testdata/printvars@"+modVersion)
	bin := filepath.Join(installed, "printvars")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if got := classify(bin, unlinkedVersion); got != SourceModule {
		t.Errorf("go install %s@%s: %v, want module", modPath, modVersion, got)
	}
}

// noGitConfig keeps the git a build in the checkout runs for its VCS stamp
// away from the user's and the system's git config. HOME stays: GOCACHE and
// GOENV are found through it.
var noGitConfig = []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}

// fileURL is the file:// URL GOPROXY takes for a local directory.
func fileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/… on Windows
	}
	return "file://" + p
}
