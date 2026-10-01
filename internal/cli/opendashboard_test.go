package cli

import (
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/testutil"
)

// A low-entropy fake: the tests never see a real launch token.
const launchURLForTest = "http://127.0.0.1:52117/?token=launch-token-for-tests"

// stubOpener applies one platform's opener rules and records what reaches
// the browser.
func stubOpener(t *testing.T, goos string) *[]string {
	t.Helper()
	prevGOOS, prevLaunch := browserGOOS, launchBrowser
	var got []string
	browserGOOS = goos
	launchBrowser = func(u string) error { got = append(got, u); return nil }
	t.Cleanup(func() { browserGOOS, launchBrowser = prevGOOS, prevLaunch })
	return &got
}

// redirectPages lists the redirect pages under the private directory the
// opener uses for the test's runtime dir.
func redirectPages(t *testing.T, runtimeDir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(runtimeDir, "tycswap", "tycswap-dashboard-*.html"))
	return m
}

// stubCacheDir points the opener's cache-directory seam at dir.
func stubCacheDir(t *testing.T, dir string, err error) {
	t.Helper()
	prev := userCacheDir
	userCacheDir = func() (string, error) { return dir, err }
	t.Cleanup(func() { userCacheDir = prev })
}

// On Windows the address goes to the browser as it is, and no redirect file
// is written.
func TestOpenBrowserWindowsSendsTheURLDirectly(t *testing.T) {
	run := t.TempDir()
	testutil.Setenv(t, "XDG_RUNTIME_DIR", run)
	got := stubOpener(t, "windows")
	if err := openBrowser(launchURLForTest); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0] != launchURLForTest {
		t.Fatalf("browser got %q, want exactly the launch URL", *got)
	}
	if files := redirectPages(t, run); len(files) != 0 {
		t.Errorf("redirect file written on Windows: %v", files)
	}
}

// checkRedirectPage asserts u is an escaped file:/// URL of a 0600 redirect
// page under dir (0700) that redirects to the launch URL, with no token in
// the URL handed to the launcher.
func checkRedirectPage(t *testing.T, goos, u, dir string) {
	t.Helper()
	if !strings.HasPrefix(u, "file:///") || strings.ContainsAny(u, ` \`) || strings.Contains(u, "token=") {
		t.Fatalf("%s: browser got %q, want an escaped file:/// URL without the token", goos, u)
	}
	parsed, err := neturl.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(parsed.Path) != dir {
		t.Errorf("%s: redirect page %q is not under the private directory %q", goos, parsed.Path, dir)
	}
	if !strings.HasPrefix(filepath.Base(parsed.Path), "tycswap-dashboard-") {
		t.Errorf("%s: redirect file %q lacks the brand prefix", goos, parsed.Path)
	}
	page, err := os.ReadFile(parsed.Path)
	if err != nil {
		t.Fatalf("%s: the URL does not name the redirect file: %v", goos, err)
	}
	if !strings.Contains(string(page), `content="0;url=`+launchURLForTest+`"`) {
		t.Errorf("%s: redirect page = %s", goos, page)
	}
	if fi, err := os.Stat(parsed.Path); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("%s: redirect page mode = %o, want 600", goos, fi.Mode().Perm())
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("%s: private directory %q mode = %v (%v), want 700", goos, dir, fi, err)
	}
}

// Elsewhere (WSL included) the token stays off the command line: the browser
// gets an escaped file: URL of a 0600 page, under a 0700 directory of the
// user's own in $XDG_RUNTIME_DIR, that redirects to the address. A space in
// the directory must not break it.
func TestOpenBrowserUnixUsesAnEscapedFileURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows opens the address directly; directory and file modes are Unix")
	}
	run := filepath.Join(t.TempDir(), "run dir")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.Setenv(t, "XDG_RUNTIME_DIR", run)
	stubCacheDir(t, filepath.Join(t.TempDir(), "unused-cache"), nil)
	for _, goos := range []string{"darwin", "linux"} {
		got := stubOpener(t, goos)
		if err := openBrowser(launchURLForTest); err != nil {
			t.Fatal(err)
		}
		if len(*got) != 1 {
			t.Fatalf("%s: browser got %q", goos, *got)
		}
		checkRedirectPage(t, goos, (*got)[0], filepath.Join(run, "tycswap"))
	}
}

// Without a runtime directory the page goes under the user's cache directory,
// never the shared temp directory.
func TestOpenBrowserUnixFallsBackToTheCacheDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory behaviour")
	}
	testutil.Setenv(t, "XDG_RUNTIME_DIR", "")
	cache := t.TempDir()
	stubCacheDir(t, cache, nil)
	tmp := t.TempDir()
	testutil.Setenv(t, "TMPDIR", tmp)
	got := stubOpener(t, "linux")
	if err := openBrowser(launchURLForTest); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("browser got %q", *got)
	}
	checkRedirectPage(t, "linux", (*got)[0], filepath.Join(cache, "tycswap"))
	if m, _ := filepath.Glob(filepath.Join(tmp, "*")); len(m) != 0 {
		t.Errorf("something was written to the shared temp directory: %v", m)
	}
}

// When no private directory can be made, the token is never handed to a
// launcher (its argv is world-readable on unix): openBrowser fails, so the
// CLI tells the user to open the printed URL.
func TestOpenBrowserUnixRefusesToPassTheTokenWhenThePageCannotBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory behaviour")
	}
	testutil.Setenv(t, "XDG_RUNTIME_DIR", "")
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubCacheDir(t, filepath.Join(blocker, "cache"), nil) // MkdirAll under a regular file fails
	got := stubOpener(t, "linux")
	if err := openBrowser(launchURLForTest); err == nil {
		t.Fatal("openBrowser succeeded without a redirect page")
	}
	if len(*got) != 0 {
		t.Fatalf("the launcher was handed %q; the token must never reach a command line", *got)
	}
	// A runtime directory that cannot be used falls through to the cache
	// directory first.
	testutil.Setenv(t, "XDG_RUNTIME_DIR", filepath.Join(blocker, "run"))
	cache := t.TempDir()
	stubCacheDir(t, cache, nil)
	if err := openBrowser(launchURLForTest); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("browser got %q", *got)
	}
	checkRedirectPage(t, "linux", (*got)[0], filepath.Join(cache, "tycswap"))
}

func TestFileURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/var/folders/x/T/tycswap-dashboard-1.html", "file:///var/folders/x/T/tycswap-dashboard-1.html"},
		{"/tmp/a b/c.html", "file:///tmp/a%20b/c.html"},
		{`C:\Users\USER~1\AppData\Local\Temp\tycswap-dashboard-1.html`, "file:///C:/Users/USER~1/AppData/Local/Temp/tycswap-dashboard-1.html"},
		{`C:\Users\Jane Doe\AppData\Local\Temp\x.html`, "file:///C:/Users/Jane%20Doe/AppData/Local/Temp/x.html"},
	} {
		if got := fileURL(tc.in); got != tc.want {
			t.Errorf("fileURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWebCommandFlags(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"web", "--port"}, "argument --port: expected one argument"},
		{[]string{"web", "--port", "70000"}, "argument --port: invalid int value"},
		{[]string{"web", "--interval=0"}, "argument --interval: invalid number of seconds"},
		{[]string{"web", "--interval", "0.5"}, "argument --interval: invalid number of seconds: '0.5' (1-3600)"},
		{[]string{"web", "--interval", "3601"}, "argument --interval: invalid number of seconds"},
		{[]string{"web", "--remote"}, "unrecognized arguments: --remote"},
	} {
		code, _, errOut := runCLI(t, tc.argv, false, false)
		if code != 2 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: exit %d stderr %q, want 2 and %q", tc.argv, code, errOut, tc.want)
		}
	}
	code, out, _ := runCLI(t, []string{"web", "--help"}, false, false)
	if code != 0 || !strings.Contains(out, "usage: tycswap web") || !strings.Contains(out, "--no-open") {
		t.Errorf("help: exit %d out %q", code, out)
	}
}
