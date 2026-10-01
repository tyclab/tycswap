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

func dashboardFiles(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(os.TempDir(), "tycswap-dashboard-*.html"))
	return m
}

// On Windows the address goes to the browser as it is, and no redirect file
// is written.
func TestOpenBrowserWindowsSendsTheURLDirectly(t *testing.T) {
	testutil.Setenv(t, "TMPDIR", t.TempDir())
	got := stubOpener(t, "windows")
	if err := openBrowser(launchURLForTest); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0] != launchURLForTest {
		t.Fatalf("browser got %q, want exactly the launch URL", *got)
	}
	if files := dashboardFiles(t); len(files) != 0 {
		t.Errorf("redirect file written on Windows: %v", files)
	}
}

// Elsewhere (WSL included) the token stays off the command line: the browser
// gets an escaped file: URL of a 0600 page that redirects to the address. A
// space in the temp directory must not break it.
func TestOpenBrowserUnixUsesAnEscapedFileURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows opens the address directly; TMPDIR and file modes are Unix")
	}
	tmp := filepath.Join(t.TempDir(), "tmp dir")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.Setenv(t, "TMPDIR", tmp)
	for _, goos := range []string{"darwin", "linux"} {
		got := stubOpener(t, goos)
		if err := openBrowser(launchURLForTest); err != nil {
			t.Fatal(err)
		}
		if len(*got) != 1 {
			t.Fatalf("%s: browser got %q", goos, *got)
		}
		u := (*got)[0]
		if !strings.HasPrefix(u, "file:///") || strings.ContainsAny(u, ` \`) || strings.Contains(u, "token=") {
			t.Fatalf("%s: browser got %q, want an escaped file:/// URL without the token", goos, u)
		}
		parsed, err := neturl.Parse(u)
		if err != nil {
			t.Fatal(err)
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
	}
}

// When the redirect page cannot be written, the plain URL is the last resort.
func TestOpenBrowserFallsBackToThePlainURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix temp-dir behaviour")
	}
	testutil.Setenv(t, "TMPDIR", filepath.Join(t.TempDir(), "missing"))
	got := stubOpener(t, "linux")
	if err := openBrowser(launchURLForTest); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0] != launchURLForTest {
		t.Fatalf("browser got %q, want the plain URL", *got)
	}
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
