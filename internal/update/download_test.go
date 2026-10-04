package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tyclab/tycswap/internal/platform"
)

// downloadServer is an https stand-in for GitHub: the latest release at
// /latest, and the release's files at /download/<tag>/<name>.
type downloadServer struct {
	t     *testing.T
	srv   *httptest.Server
	tag   string
	files map[string][]byte // name → body, SHA256SUMS included
	hits  atomic.Int32
}

// sumsFor renders a SHA256SUMS file for the assets, the way the release
// workflow's sha256sum writes it.
func sumsFor(assets map[string][]byte) []byte {
	var b bytes.Buffer
	for name, body := range assets {
		sum := sha256.Sum256(body)
		b.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	return b.Bytes()
}

// newDownloadServer publishes tag with assets and their SHA256SUMS; sums, when
// non-nil, replaces the file (nil means "generate it").
func newDownloadServer(t *testing.T, tag string, assets map[string][]byte, sums []byte) *downloadServer {
	t.Helper()
	rs := &downloadServer{t: t, tag: tag, files: map[string][]byte{}}
	for name, body := range assets {
		rs.files[name] = body
	}
	if sums == nil {
		sums = sumsFor(assets)
	}
	rs.files["SHA256SUMS"] = sums
	rs.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.hits.Add(1)
		if r.URL.Path == "/latest" {
			_, _ = io.WriteString(w, `{"tag_name":"`+rs.tag+`"}`)
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, "/download/"+rs.tag+"/")
		body, have := rs.files[name]
		if !ok || !have {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

// withReleasesURL points the package's ReleasesURL at url for the test.
func withReleasesURL(t *testing.T, url string) {
	t.Helper()
	prev := ReleasesURL
	ReleasesURL = url
	t.Cleanup(func() { ReleasesURL = prev })
}

// installedBinary makes the running binary a release build (withRelease)
// and writes a fake copy of it into a fresh, writable directory that is not
// a Go bin directory: the download shape.
func installedBinary(t *testing.T, name string) (dir, exe string) {
	t.Helper()
	withRelease(t)
	dir = t.TempDir()
	exe = filepath.Join(dir, name)
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, exe
}

// downloadUpgrader upgrades from rs: the package's Endpoint and ReleasesURL
// point at it for the test.
func downloadUpgrader(rs *downloadServer, version string) (Upgrader, *bytes.Buffer, *bytes.Buffer) {
	withEndpoint(rs.t, rs.srv.URL+"/latest")
	withReleasesURL(rs.t, rs.srv.URL)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	u := Upgrader{
		Getenv:     func(string) string { return "" },
		HomeDir:    "/nonexistent-home",
		Stdout:     stdout,
		Stderr:     stderr,
		Version:    version,
		Arch:       "amd64",
		HTTPClient: rs.srv.Client(),
		Run: func(context.Context, string, []string, io.Writer, io.Writer) (int, error) {
			panic("go install must not run for the download shape")
		},
	}
	return u, stdout, stderr
}

func TestAssetName(t *testing.T) {
	cases := []struct {
		plat platform.Platform
		arch string
		want string
	}{
		{platform.MacOS, "arm64", "tycswap_v0.7.0_darwin_arm64"},
		{platform.Linux, "amd64", "tycswap_v0.7.0_linux_amd64"},
		{platform.WSL, "arm64", "tycswap_v0.7.0_linux_arm64"},
		{platform.Windows, "amd64", "tycswap_v0.7.0_windows_amd64.exe"},
		{platform.Platform(99), "amd64", ""},
	}
	for _, tc := range cases {
		if got := AssetName("v0.7.0", tc.plat, tc.arch); got != tc.want {
			t.Errorf("AssetName(%v,%s) = %q, want %q", tc.plat, tc.arch, got, tc.want)
		}
	}
}

func TestDownloadUpgrade_ReplacesBinaryAfterVerifying(t *testing.T) {
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{"tycswap_v0.7.0_linux_amd64": []byte("new build bytes")}, nil)
	dir, exe := installedBinary(t, "tycswap")
	u, stdout, stderr := downloadUpgrader(rs, "v0.6.0")

	if code := u.SelfUpgrade(exe, platform.Linux); code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, stderr.String())
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new build bytes" {
		t.Errorf("binary = %q, want the downloaded build", got)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o111 == 0 && os.PathSeparator == '/' {
		t.Errorf("binary mode %v is not executable", fi.Mode())
	}
	if !strings.Contains(stdout.String(), "Updated tycswap 0.6.0 → 0.7.0") {
		t.Errorf("stdout = %q", stdout.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

// What SHA256SUMS says decides: a mismatch, no line, two lines, a malformed
// hash or no file at all leave the binary as it is.
func TestDownloadUpgrade_RefusesAnUnverifiedBuild(t *testing.T) {
	const asset = "tycswap_v0.7.0_linux_amd64"
	body := []byte("tampered")
	good := sumsFor(map[string][]byte{asset: body})
	for _, tc := range []struct {
		name string
		sums []byte
		want string
	}{
		{"checksum mismatch", []byte(strings.Repeat("0", 64) + "  " + asset + "\n"), "Checksum mismatch"},
		{"no line for the asset", []byte(strings.Repeat("0", 64) + "  tycswap_v0.7.0_darwin_arm64\n"), "SHA256SUMS has no line for " + asset},
		{"two lines", append(append([]byte{}, good...), good...), "SHA256SUMS has 2 lines for " + asset},
		{"malformed hash", []byte("abc  " + asset + "\n"), "malformed hash"},
		{"no SHA256SUMS", nil, "SHA256SUMS could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := newDownloadServer(t, "v0.7.0", map[string][]byte{asset: body}, tc.sums)
			if tc.sums == nil {
				delete(rs.files, "SHA256SUMS")
			}
			dir, exe := installedBinary(t, "tycswap")
			u, _, stderr := downloadUpgrader(rs, "v0.6.0")
			if code := u.SelfUpgrade(exe, platform.Linux); code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
			if got, _ := os.ReadFile(exe); string(got) != "old build" {
				t.Errorf("binary was replaced: %q", got)
			}
			if !strings.Contains(stderr.String(), tc.want) || !strings.Contains(stderr.String(), "refusing") {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("temp download not cleaned up: %v", entries)
			}
		})
	}
	// A "*name" line (sha256sum's binary mode) names the asset too.
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{asset: body}, bytes.Replace(good, []byte("  "), []byte(" *"), 1))
	_, exe := installedBinary(t, "tycswap")
	u, _, stderr := downloadUpgrader(rs, "v0.6.0")
	if code := u.SelfUpgrade(exe, platform.Linux); code != 0 {
		t.Fatalf("binary-mode line: exit %d; stderr=%q", code, stderr.String())
	}
}

func TestDownloadUpgrade_AlreadyCurrentSkipsDownload(t *testing.T) {
	rs := newDownloadServer(t, "v0.6.0", map[string][]byte{"tycswap_v0.6.0_linux_amd64": []byte("same")}, nil)
	_, exe := installedBinary(t, "tycswap")
	u, stdout, _ := downloadUpgrader(rs, "v0.6.0")

	if code := u.SelfUpgrade(exe, platform.Linux); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout.String(), "0.6.0 is the latest version") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if rs.hits.Load() != 1 {
		t.Errorf("%d requests, want just the release lookup", rs.hits.Load())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old build" {
		t.Error("binary must be untouched when already current")
	}
}

func TestDownloadUpgrade_UnknownVersionAlwaysInstalls(t *testing.T) {
	rs := newDownloadServer(t, "v0.0.1", map[string][]byte{"tycswap_v0.0.1_linux_amd64": []byte("dev→release")}, nil)
	_, exe := installedBinary(t, "tycswap")
	u, stdout, _ := downloadUpgrader(rs, "dev")

	if code := u.SelfUpgrade(exe, platform.Linux); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout.String(), "Updated tycswap dev → 0.0.1") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestDownloadUpgrade_NoAssetForPlatform(t *testing.T) {
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{"tycswap_v0.7.0_linux_amd64": []byte("x")}, nil)
	_, exe := installedBinary(t, "tycswap")
	u, _, stderr := downloadUpgrader(rs, "v0.6.0")

	if code := u.SelfUpgrade(exe, platform.MacOS); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "SHA256SUMS has no line for tycswap_v0.7.0_darwin_amd64") || !strings.Contains(stderr.String(), rs.srv.URL) {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestDownloadUpgrade_EndpointUnreachable(t *testing.T) {
	rs := newDownloadServer(t, "v0.7.0", nil, nil)
	_, exe := installedBinary(t, "tycswap")
	u, _, stderr := downloadUpgrader(rs, "v0.6.0")
	withEndpoint(t, "https://127.0.0.1:1/latest")

	if code := u.SelfUpgrade(exe, platform.Linux); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Could not read the newest release") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// The downloads are https, every hop of them: plain http is refused before
// anything is fetched, and so is a redirect to it.
func TestDownloadUpgrade_HTTPSOnly(t *testing.T) {
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{"tycswap_v0.7.0_linux_amd64": []byte("x")}, nil)
	_, exe := installedBinary(t, "tycswap")
	u, _, stderr := downloadUpgrader(rs, "v0.6.0")
	withReleasesURL(t, "http://127.0.0.1:1")
	if code := u.SelfUpgrade(exe, platform.Linux); code != 1 || !strings.Contains(stderr.String(), "not https") {
		t.Errorf("http downloads: exit %d, stderr %q", code, stderr.String())
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("from plain http"))
	}))
	t.Cleanup(plain.Close)
	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			_, _ = io.WriteString(w, `{"tag_name":"v0.7.0"}`)
			return
		}
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	u, _, stderr = downloadUpgrader(rs, "v0.6.0")
	withEndpoint(t, redirecting.URL+"/latest")
	withReleasesURL(t, redirecting.URL)
	u.HTTPClient = redirecting.Client()
	if code := u.SelfUpgrade(exe, platform.Linux); code != 1 || !strings.Contains(stderr.String(), "non-https") {
		t.Errorf("redirect to http: exit %d, stderr %q", code, stderr.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old build" {
		t.Errorf("binary = %q", got)
	}
}

// An asset larger than the cap is refused before it is verified or moved.
func TestDownloadUpgrade_SizeCap(t *testing.T) {
	prev := maxAssetBytes
	maxAssetBytes = 8
	t.Cleanup(func() { maxAssetBytes = prev })
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{"tycswap_v0.7.0_linux_amd64": []byte("twelve bytes")}, nil)
	dir, exe := installedBinary(t, "tycswap")
	u, _, stderr := downloadUpgrader(rs, "v0.6.0")
	if code := u.SelfUpgrade(exe, platform.Linux); code != 1 || !strings.Contains(stderr.String(), "larger than 8 bytes") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp download not cleaned up: %v", entries)
	}
}

// A SHA256SUMS larger than its cap is refused, even with the asset's line in
// it: the file is read into memory.
func TestDownloadUpgrade_SumsSizeCap(t *testing.T) {
	const asset = "tycswap_v0.7.0_linux_amd64"
	body := []byte("new build")
	sums := append(sumsFor(map[string][]byte{asset: body}), bytes.Repeat([]byte("#\n"), maxSumsBytes/2+1)...)
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{asset: body}, sums)
	dir, exe := installedBinary(t, "tycswap")
	u, _, stderr := downloadUpgrader(rs, "v0.6.0")
	if code := u.SelfUpgrade(exe, platform.Linux); code != 1 || !strings.Contains(stderr.String(), "SHA256SUMS could not be read (larger than "+strconv.Itoa(maxSumsBytes)+" bytes)") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old build" {
		t.Errorf("binary = %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("leftovers: %v", entries)
	}
}

// A release whose tag is not a version is refused before anything is
// downloaded: it would slip past the downgrade guard, and for a binary of an
// unknown version it would be installed.
func TestDownloadUpgrade_RefusesAnUnusableTag(t *testing.T) {
	for _, current := range []string{"v0.6.0", "dev"} {
		rs := newDownloadServer(t, "nightly", map[string][]byte{"tycswap_nightly_linux_amd64": []byte("nightly build")}, nil)
		_, exe := installedBinary(t, "tycswap")
		u, _, stderr := downloadUpgrader(rs, current)
		if code := u.SelfUpgrade(exe, platform.Linux); code != 1 || !strings.Contains(stderr.String(), `unusable version "nightly"`) {
			t.Errorf("running %s: exit %d, stderr %q", current, code, stderr.String())
		}
		if got, _ := os.ReadFile(exe); string(got) != "old build" {
			t.Errorf("running %s: binary = %q", current, got)
		}
		if n := rs.hits.Load(); n != 1 {
			t.Errorf("running %s: %d requests, want just the release lookup", current, n)
		}
	}
}

// A download follows at most nine redirects (ten requests, as Go's own
// default): GitHub needs one, and a loop ends instead of running until the
// timeout.
func TestDownloadUpgrade_RedirectCap(t *testing.T) {
	const asset = "tycswap_v0.7.0_linux_amd64"
	body := []byte("new build")
	for _, tc := range []struct {
		redirects int
		ok        bool
	}{{9, true}, {10, false}} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/latest":
				_, _ = io.WriteString(w, `{"tag_name":"v0.7.0"}`)
			case r.URL.Path == "/download/v0.7.0/SHA256SUMS":
				_, _ = w.Write(sumsFor(map[string][]byte{asset: body}))
			case r.URL.Path == "/download/v0.7.0/"+asset:
				http.Redirect(w, r, "/hop/1", http.StatusFound)
			case strings.HasPrefix(r.URL.Path, "/hop/"):
				n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
				if n < tc.redirects {
					http.Redirect(w, r, "/hop/"+strconv.Itoa(n+1), http.StatusFound)
					return
				}
				_, _ = w.Write(body)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)
		_, exe := installedBinary(t, "tycswap")
		u, _, stderr := downloadUpgrader(&downloadServer{t: t, srv: srv, tag: "v0.7.0"}, "v0.6.0")
		code := u.SelfUpgrade(exe, platform.Linux)
		got, _ := os.ReadFile(exe)
		switch {
		case tc.ok && (code != 0 || string(got) != string(body)):
			t.Errorf("%d redirects: exit %d, binary %q, stderr %q", tc.redirects, code, got, stderr.String())
		case !tc.ok && (code != 1 || string(got) != "old build" || !strings.Contains(stderr.String(), "too many redirects")):
			t.Errorf("%d redirects: exit %d, binary %q, stderr %q", tc.redirects, code, got, stderr.String())
		}
	}
}

// A binary in the Nix store is never downloaded over, even where this
// process could write: the store belongs to the package manager.
func TestDownloadUpgrade_NixStoreGuard(t *testing.T) {
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{"tycswap_v0.7.0_linux_amd64": []byte("new build")}, nil)
	dir, exe := installedBinary(t, "tycswap")
	noEnv := func(string) string { return "" }
	if p := UpgradePlan(SourceRelease, exe, noEnv, t.TempDir()); p.Method != MethodDownload {
		t.Fatalf("a writable directory outside the store: %+v", p)
	}
	// The plan compares the resolved path: a temporary directory is behind a
	// link on macOS (/var → /private/var) and a short name on Windows.
	store, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	prev := nixStore
	nixStore = filepath.ToSlash(store) + "/"
	t.Cleanup(func() { nixStore = prev })
	if p := UpgradePlan(SourceRelease, exe, noEnv, t.TempDir()); p.Method != MethodPackageManager || p.Manager != "Nix" {
		t.Errorf("a binary in the store: %+v", p)
	}
	u, _, stderr := downloadUpgrader(rs, "v0.6.0")
	if code := u.SelfUpgrade(exe, platform.Linux); code != 1 || !strings.Contains(stderr.String(), "update it with Nix (it is in the Nix store)") ||
		strings.Contains(stderr.String(), ReleasesURL) {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old build" || rs.hits.Load() != 0 {
		t.Errorf("binary = %q after %d requests", got, rs.hits.Load())
	}
}

func TestDownloadUpgrade_WindowsRenameDance(t *testing.T) {
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{"tycswap_v0.7.0_windows_amd64.exe": []byte("new exe")}, nil)
	dir, exe := installedBinary(t, "tycswap.exe")
	u, stdout, stderr := downloadUpgrader(rs, "v0.6.0")

	if code := u.SelfUpgrade(exe, platform.Windows); code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, stderr.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "new exe" {
		t.Errorf("exe = %q", got)
	}
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error(".old should be removed when the OS allows it")
	}
	if !strings.Contains(stdout.String(), "Updated") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("leftovers: %v", entries)
	}
}

func TestReplaceBinary_WindowsRestoresOnFailure(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	if err := os.WriteFile(exe, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	// newPath does not exist, so the second rename fails and the first must be undone.
	if err := replaceBinary(exe, filepath.Join(dir, "missing.tmp"), true); err == nil {
		t.Fatal("expected an error")
	}
	if got, _ := os.ReadFile(exe); string(got) != "current" {
		t.Errorf("exe = %q, want the original restored", got)
	}
	RemoveStaleBinary(exe)
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error(".old left behind")
	}
}

// The <exe>.old a Windows upgrade leaves behind goes at the next start.
func TestRemoveStaleBinary(t *testing.T) {
	_, exe := installedBinary(t, "tycswap.exe")
	if err := os.WriteFile(exe+".old", []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	RemoveStaleBinary(exe)
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Error(".old left behind")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Errorf("the binary itself went: %v", err)
	}
	RemoveStaleBinary("") // no path, nothing to do
}

// DESIGN A36's plan for each build source and place.
func TestUpgradePlan(t *testing.T) {
	home := t.TempDir()
	getenv := func(string) string { return "" }
	goBin := filepath.Join(home, "go", "bin", "tycswap")
	_, writable := installedBinary(t, "tycswap")
	cases := []struct {
		name string
		src  BuildSource
		exe  string
		want Plan
	}{
		{"checkout, anywhere", SourceCheckout, writable, Plan{Method: MethodCheckout}},
		{"module in a Go bin directory", SourceModule, goBin, Plan{Method: MethodGoInstall}},
		{"module elsewhere never downloads", SourceModule, writable, Plan{Method: MethodManual}},
		{"release in a writable place", SourceRelease, writable, Plan{Method: MethodDownload}},
		{"release in the Nix store", SourceRelease, "/nix/store/0000-tycswap/bin/tycswap", Plan{Method: MethodPackageManager, Manager: "Nix"}},
		{"release in a Homebrew Cellar", SourceRelease, "/opt/homebrew/Cellar/tycswap/0.6.0/bin/tycswap", Plan{Method: MethodPackageManager, Manager: "Homebrew"}},
		{"release in Scoop's apps", SourceRelease, `C:/Users/u/scoop/apps/tycswap/current/tycswap.exe`, Plan{Method: MethodPackageManager, Manager: "Scoop"}},
		{"release from the Microsoft Store", SourceRelease, `C:/Program Files/WindowsApps/tycswap/tycswap.exe`, Plan{Method: MethodPackageManager, Manager: "the Microsoft Store"}},
		{"release with no path", SourceRelease, "", Plan{Method: MethodManual}},
		{"release that is not there", SourceRelease, filepath.Join(home, "missing", "tycswap"), Plan{Method: MethodManual}},
	}
	for _, c := range cases {
		if got := UpgradePlan(c.src, c.exe, getenv, home); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	if runtime.GOOS != "windows" {
		// A release started through a symbolic link is a package manager's.
		link := filepath.Join(t.TempDir(), "tycswap")
		if err := os.Symlink(writable, link); err != nil {
			t.Fatal(err)
		}
		if got := UpgradePlan(SourceRelease, link, getenv, home); got != (Plan{Method: MethodPackageManager}) {
			t.Errorf("symlinked release: %+v", got)
		}
	}
	if os.Geteuid() > 0 {
		// Without write access to the directory or the file, by hand.
		dir := t.TempDir()
		exe := filepath.Join(dir, "tycswap")
		if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		if got := UpgradePlan(SourceRelease, exe, getenv, home); got.Method != MethodManual {
			t.Errorf("read-only directory: %+v, want manual", got)
		}
		ro := filepath.Join(t.TempDir(), "tycswap")
		if err := os.WriteFile(ro, []byte("old build"), 0o555); err != nil {
			t.Fatal(err)
		}
		if got := UpgradePlan(SourceRelease, ro, getenv, home); got.Method != MethodManual {
			t.Errorf("read-only file: %+v, want manual", got)
		}
		if entries, _ := os.ReadDir(filepath.Dir(writable)); len(entries) != 1 {
			t.Errorf("deciding the plan wrote into the binary's directory: %v", entries)
		}
	}
}

// What a package manager's binary is told to update with.
func TestPlanUpdater(t *testing.T) {
	for manager, want := range map[string]string{
		"":         "the package manager that installed it",
		"Nix":      "Nix (it is in the Nix store)",
		"Homebrew": "Homebrew, which installed it",
	} {
		if got := (Plan{Method: MethodPackageManager, Manager: manager}).Updater(); got != want {
			t.Errorf("%q: %q, want %q", manager, got, want)
		}
	}
}

// releaseBuild is what the release workflow stamps: no VCS stamp and no
// module version. The tag it links into internal/version is not in the
// build info (Go leaves -ldflags out whenever -trimpath is set,
// go.dev/issue/52372): withRelease links it.
func releaseBuild() (*debug.BuildInfo, bool) {
	return &debug.BuildInfo{
		Path: "github.com/tyclab/tycswap/cmd/tycswap",
		Main: debug.Module{Path: "github.com/tyclab/tycswap", Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "-buildmode", Value: "exe"},
			{Key: "-trimpath", Value: "true"},
			{Key: "CGO_ENABLED", Value: "0"},
		},
	}, true
}

// withRelease makes the running binary a release build of v0.6.0.
func withRelease(t *testing.T) {
	t.Helper()
	withBuild(t, releaseBuild)
	withLinkedVersion(t, "v0.6.0")
}

// A release binary is upgraded by downloading the next release over it, not
// told to `go install` and not taken for a checkout build.
func TestSelfUpgrade_ReleaseBuildDownloads(t *testing.T) {
	rs := newDownloadServer(t, "v0.7.0", map[string][]byte{"tycswap_v0.7.0_linux_amd64": []byte("next release")}, nil)
	_, exe := installedBinary(t, "tycswap")
	u, stdout, stderr := downloadUpgrader(rs, "v0.6.0")
	if code := u.SelfUpgrade(exe, platform.Linux); code != 0 {
		t.Fatalf("exit %d; stderr=%q", code, stderr.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "next release" || !strings.HasPrefix(stdout.String(), "Updated tycswap 0.6.0 → 0.7.0") {
		t.Errorf("binary %q, stdout %q", got, stdout.String())
	}
}
