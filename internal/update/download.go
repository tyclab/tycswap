// The download shape of `tycswap upgrade` (DESIGN A24, A36): a binary that
// was not installed with `go install` and is not a checkout build — a release
// binary, or one copied out of a Go bin directory — is upgraded by
// downloading the newest release's build for this OS and architecture,
// checking it against that release's SHA256SUMS and renaming it over the
// running file.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/tyclab/tycswap/internal/platform"
)

// DownloadTimeout bounds one release lookup plus one asset download.
var DownloadTimeout = 5 * time.Minute

// BinaryName is the release asset stem.
const BinaryName = "tycswap"

// Method is how SelfUpgrade upgrades a binary that is not a checkout build.
type Method int

const (
	// MethodManual: the binary is upgraded by hand (it lives in the Nix
	// store, or in a directory this process cannot write).
	MethodManual Method = iota
	// MethodGoInstall: `go install <ModulePath>@latest` (print-only on Windows).
	MethodGoInstall
	// MethodDownload: the release asset downloaded over the running binary.
	MethodDownload
)

// UpgradeMethod is how SelfUpgrade upgrades exePath, the checkout test aside:
// `go install` in a Go bin directory; else a download over the binary when
// it sits outside the Nix store in a directory this process can write; else
// by hand.
func UpgradeMethod(exePath string, getenv func(string) string, homeDir string) Method {
	if DetectInstallShape(exePath, getenv, homeDir) == ShapeGoInstall {
		return MethodGoInstall
	}
	if Downloadable(exePath) {
		return MethodDownload
	}
	return MethodManual
}

// nixStore is the Nix store's prefix; a variable so a test can put a
// directory it can write in its place.
var nixStore = "/nix/store/"

// Downloadable reports whether a release may be downloaded over exePath: a
// known path outside the Nix store (a store path is read-only and belongs to
// the package manager) in a directory this process can create a file in.
func Downloadable(exePath string) bool {
	if exePath == "" || strings.HasPrefix(filepath.ToSlash(exePath), nixStore) {
		return false
	}
	f, err := os.CreateTemp(filepath.Dir(exePath), "."+BinaryName+"-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// AssetName is the release asset for tag on plat/arch, as the release
// workflow names it: tycswap_<tag>_<os>_<arch>, with .exe on Windows. "" when
// the platform has no published build.
func AssetName(tag string, plat platform.Platform, arch string) string {
	var goos string
	switch plat {
	case platform.MacOS:
		goos = "darwin"
	case platform.Linux, platform.WSL:
		goos = "linux"
	case platform.Windows:
		goos = "windows"
	default:
		return ""
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	name := BinaryName + "_" + tag + "_" + goos + "_" + arch
	if plat == platform.Windows {
		name += ".exe"
	}
	return name
}

func (u Upgrader) httpClient() *http.Client {
	c := http.DefaultClient
	if u.HTTPClient != nil {
		c = u.HTTPClient
	}
	// The bytes are about to become this executable: every hop, redirects
	// included (GitHub sends release downloads to its storage host), is https.
	cp := *c
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("redirect to a non-https URL")
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &cp
}

func (u Upgrader) endpoint() string {
	if u.Endpoint != "" {
		return u.Endpoint
	}
	return Endpoint
}

func (u Upgrader) releasesURL() string {
	if u.ReleasesURL != "" {
		return u.ReleasesURL
	}
	return ReleasesURL
}

// downloadUpgrade is SelfUpgrade for the download shape. It never returns an
// error: each failure prints what happened and how to finish by hand, and
// returns 1; success and "already current" return 0.
func (u Upgrader) downloadUpgrade(exePath string, plat platform.Platform) int {
	manual := func(format string, args ...any) int {
		fmt.Fprintf(u.stderr(), format+"\n", args...)
		fmt.Fprintf(u.stderr(), "To upgrade manually, download the build for this machine from:\n  %s\n", u.releasesURL())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), DownloadTimeout)
	defer cancel()
	client := u.httpClient()
	tag, err := fetchLatestTag(ctx, client, u.endpoint())
	if err != nil {
		return manual("Could not read the newest release: %v", err)
	}
	// An unusable tag must not skip the downgrade guard below.
	if !semver.IsValid(tag) {
		return manual("The newest release carries an unusable version %q; refusing to install it.", tag)
	}
	current := u.Version
	if semver.IsValid(current) && semver.Compare(tag, current) <= 0 {
		fmt.Fprintf(u.stdout(), "tycswap %s is the latest version.\n", strings.TrimPrefix(current, "v"))
		return 0
	}
	asset := AssetName(tag, plat, u.Arch)
	if asset == "" {
		return manual("No release build is published for this platform.")
	}
	base := strings.TrimRight(u.releasesURL(), "/") + "/download/" + tag + "/"
	if b, err := url.Parse(base); err != nil || b.Scheme != "https" || b.Host == "" {
		return manual("The release downloads at %s are not https; refusing.", base)
	}
	want, err := u.checksum(ctx, client, base+"SHA256SUMS", asset)
	if err != nil {
		return manual("Release %s: %v; refusing to install an unverifiable build.", tag, err)
	}

	// Land the download next to the binary so the final rename stays on one
	// filesystem and is atomic where the OS allows it.
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, "."+BinaryName+"-upgrade-*")
	if err != nil {
		return manual("Cannot write to %s (%v).", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // a no-op after the rename succeeded

	if err := download(ctx, client, base+asset, tmp, maxAssetBytes); err != nil {
		tmp.Close()
		return manual("Download of %s failed: %v", asset, err)
	}
	if err := tmp.Close(); err != nil {
		return manual("Download of %s failed: %v", asset, err)
	}
	got, err := fileSHA256(tmpPath)
	if err != nil {
		return manual("Could not verify %s: %v", asset, err)
	}
	if got != want {
		return manual("Checksum mismatch for %s (got %s, want %s) — refusing to install it.", asset, got, want)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return manual("Could not mark %s executable: %v", asset, err)
	}
	if err := replaceBinary(exePath, tmpPath, plat == platform.Windows); err != nil {
		return manual("Could not replace %s: %v", exePath, err)
	}
	from := strings.TrimPrefix(current, "v")
	if from == "" {
		from = "unknown"
	}
	fmt.Fprintf(u.stdout(), "Updated tycswap %s → %s (%s).\n", from, strings.TrimPrefix(tag, "v"), exePath)
	return 0
}

// maxAssetBytes bounds one release download. The binary is about 10 MB;
// 256 MiB is generous and still refuses an endless stream before it fills
// the filesystem that holds the binary.
var maxAssetBytes int64 = 256 << 20

// maxSumsBytes bounds the SHA256SUMS file.
const maxSumsBytes = 1 << 20

// checksum reads the release's SHA256SUMS and returns the hash on the one
// line that names asset ("<64 hex>  <name>", or " *<name>" for binary mode).
// No line, two lines, or a malformed hash is an error: the build cannot be
// verified.
func (u Upgrader) checksum(ctx context.Context, client *http.Client, sumsURL, asset string) (string, error) {
	var buf bytes.Buffer
	if err := download(ctx, client, sumsURL, &buf, maxSumsBytes); err != nil {
		return "", fmt.Errorf("SHA256SUMS could not be read (%v)", err)
	}
	var found []string
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		found = append(found, strings.ToLower(fields[0]))
	}
	switch {
	case len(found) == 0:
		return "", fmt.Errorf("SHA256SUMS has no line for %s", asset)
	case len(found) > 1:
		return "", fmt.Errorf("SHA256SUMS has %d lines for %s", len(found), asset)
	}
	if h, err := hex.DecodeString(found[0]); err != nil || len(h) != sha256.Size {
		return "", fmt.Errorf("SHA256SUMS carries a malformed hash for %s", asset)
	}
	return found[0], nil
}

// download copies url's body into dst, at most limit bytes.
func download(ctx context.Context, client *http.Client, url string, dst io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("larger than %d bytes", limit)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// replaceBinary swaps newPath over exePath. On Unix a rename over the running
// file is atomic and the old inode lives on until the process exits. Windows
// forbids replacing a running .exe but allows renaming it, so the running
// file is moved aside to <exe>.old first and put back if the second rename
// fails; the leftover is removed on a later run by RemoveStaleBinary (this
// process cannot delete itself).
func replaceBinary(exePath, newPath string, windows bool) error {
	if !windows {
		return os.Rename(newPath, exePath)
	}
	old := exePath + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exePath, old); err != nil {
		return err
	}
	if err := os.Rename(newPath, exePath); err != nil {
		_ = os.Rename(old, exePath) // put the working binary back
		return err
	}
	_ = os.Remove(old) // succeeds on Unix test runs; fails harmlessly on a live Windows binary
	return nil
}

// RemoveStaleBinary deletes the <exe>.old a Windows upgrade leaves behind.
// Safe to call on every start; errors are ignored.
func RemoveStaleBinary(exePath string) {
	if exePath == "" {
		return
	}
	_ = os.Remove(exePath + ".old")
}
