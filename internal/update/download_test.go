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
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tyclab/tycswap/internal/platform"
)

// downloadServer is an https stand-in for GitHub: the latest release at
// /latest, and the release's files at /download/<tag>/<name>.
type downloadServer struct {
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
	rs := &downloadServer{tag: tag, files: map[string][]byte{}}
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

// installedBinary writes a fake current binary into a fresh, writable
// directory that is not a Go bin directory: the download shape.
func installedBinary(t *testing.T, name string) (dir, exe string) {
	t.Helper()
	dir = t.TempDir()
	exe = filepath.Join(dir, name)
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, exe
}

func downloadUpgrader(rs *downloadServer, version string) (Upgrader, *bytes.Buffer, *bytes.Buffer) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	u := Upgrader{
		Getenv:      func(string) string { return "" },
		HomeDir:     "/nonexistent-home",
		Stdout:      stdout,
		Stderr:      stderr,
		Version:     version,
		Arch:        "amd64",
		Endpoint:    rs.srv.URL + "/latest",
		ReleasesURL: rs.srv.URL,
		HTTPClient:  rs.srv.Client(),
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
	u.Endpoint = "https://127.0.0.1:1/latest"

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
	u.ReleasesURL = "http://127.0.0.1:1"
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
	u.Endpoint, u.ReleasesURL, u.HTTPClient = redirecting.URL+"/latest", redirecting.URL, redirecting.Client()
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

func TestUpgradeMethod(t *testing.T) {
	home := t.TempDir()
	getenv := func(string) string { return "" }
	binDir := filepath.Join(home, "go", "bin")
	if got := UpgradeMethod(filepath.Join(binDir, "tycswap"), getenv, home); got != MethodGoInstall {
		t.Errorf("Go bin directory: %v", got)
	}
	if got := UpgradeMethod(filepath.Join(t.TempDir(), "tycswap"), getenv, home); got != MethodDownload {
		t.Errorf("writable directory: %v", got)
	}
	for _, exe := range []string{"/nix/store/0000-tycswap/bin/tycswap", "", filepath.Join(home, "missing", "tycswap")} {
		if got := UpgradeMethod(exe, getenv, home); got != MethodManual {
			t.Errorf("%q: %v, want manual", exe, got)
		}
	}
	if os.Geteuid() > 0 {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		if got := UpgradeMethod(filepath.Join(dir, "tycswap"), getenv, home); got != MethodManual {
			t.Errorf("read-only directory: %v, want manual", got)
		}
	}
}

// releaseBuild is what the release workflow stamps: no VCS, -trimpath, and
// the tag linked into internal/version.
func releaseBuild() (*debug.BuildInfo, bool) {
	return &debug.BuildInfo{
		Path: "github.com/tyclab/tycswap/cmd/tycswap",
		Main: debug.Module{Path: "github.com/tyclab/tycswap", Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "-buildmode", Value: "exe"},
			{Key: "-ldflags", Value: "-s -w -X github.com/tyclab/tycswap/internal/version.Version=v0.6.0"},
			{Key: "-trimpath", Value: "true"},
			{Key: "CGO_ENABLED", Value: "0"},
		},
	}, true
}

// A release binary is upgraded by downloading the next release over it, not
// told to `go install` and not taken for a checkout build.
func TestSelfUpgrade_ReleaseBuildDownloads(t *testing.T) {
	withBuild(t, releaseBuild)
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
