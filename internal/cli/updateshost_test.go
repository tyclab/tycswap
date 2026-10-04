// Tests for the dashboard's updates host (DESIGN A27): the release check
// against a fake release endpoint, the Claude Code check against a fake
// machine, "a failed check forgets nothing", the view, and the two applies.
package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/ccversion"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/update"
)

// hostFixture is an updatesHost over fakes: a release endpoint the test
// serves, a Claude Code status the test sets, an upgrade that prints what
// the test says.
type hostFixture struct {
	h       *updatesHost
	mu      sync.Mutex
	tag     string // what the release endpoint answers ("" → 503)
	cc      ccversion.Status
	changes int
	upCode  int
	upOut   string
	ranCmds []string
}

func newHostFixture(t *testing.T, current string) *hostFixture {
	t.Helper()
	f := &hostFixture{tag: "v0.5.0"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		tag := f.tag
		f.mu.Unlock()
		if tag == "" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	}))
	t.Cleanup(srv.Close)
	prev := update.Endpoint
	update.Endpoint = srv.URL
	t.Cleanup(func() { update.Endpoint = prev })
	checker := update.Checker{CacheDir: t.TempDir()}
	f.h = &updatesHost{
		latest: checker.Latest,
		check: func(context.Context) ccversion.Status {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.cc
		},
		upgrade: func(stdout, stderr *bytes.Buffer) int {
			f.mu.Lock()
			defer f.mu.Unlock()
			stdout.WriteString(f.upOut)
			return f.upCode
		},
		runClaude: func(_ context.Context, c ccversion.Command, in *ccversion.Installed) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.ranCmds = append(f.ranCmds, c.String())
			updated := *f.cc.Installed
			updated.Version = f.cc.Latest
			f.cc.Installed = &updated
			return "updated\n", nil
		},
		hint:    func() string { return "" },
		current: current,
		now:     func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) },
		onChange: func() {
			f.mu.Lock()
			f.changes++
			f.mu.Unlock()
		},
	}
	f.cc = ccversion.Status{Checked: true, Installed: &ccversion.Installed{Path: "/x/claude", Version: "2.1.0", Method: ccversion.Native}, Latest: "2.2.0"}
	return f
}

func (f *hostFixture) checkAndWait(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	before := f.changes
	f.mu.Unlock()
	if err := f.h.Check(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		done := f.changes > before
		f.mu.Unlock()
		if done && !f.h.View().Checking {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the check did not finish")
}

func TestUpdatesHostCheckFindsBoth(t *testing.T) {
	f := newHostFixture(t, "v0.4.0")
	v := f.h.View()
	if v.Available || v.Checking || v.CheckedAt != nil || v.App.Current != "v0.4.0" || v.ClaudeCode.State != "checking" {
		t.Fatalf("before any check: %+v app %+v cc %+v", v, v.App, v.ClaudeCode)
	}
	f.checkAndWait(t)
	v = f.h.View()
	if !v.Available || v.CheckedAt == nil {
		t.Fatalf("after check: %+v", v)
	}
	if !v.App.Available || v.App.Latest != "v0.5.0" || v.App.Error != "" {
		t.Errorf("app = %+v", v.App)
	}
	cc := v.ClaudeCode
	if !cc.Available || cc.State != "update" || cc.Installed != "2.1.0" || cc.Latest != "2.2.0" || cc.Command != "claude update" || cc.Method != "the native installer" {
		t.Errorf("claudeCode = %+v", cc)
	}
}

func TestUpdatesHostUpToDate(t *testing.T) {
	f := newHostFixture(t, "v0.5.0")
	f.mu.Lock()
	f.cc.Installed.Version = "2.2.0"
	f.mu.Unlock()
	f.checkAndWait(t)
	v := f.h.View()
	if v.Available || v.App.Available || v.ClaudeCode.State != "latest" || v.ClaudeCode.Available {
		t.Errorf("view = %+v app %+v cc %+v", v, v.App, v.ClaudeCode)
	}
	// Missing Claude Code is said, not offered.
	f.mu.Lock()
	f.cc = ccversion.Status{Checked: true}
	f.mu.Unlock()
	f.checkAndWait(t)
	cc := f.h.View().ClaudeCode
	if cc.State != "missing" || cc.Available || cc.Command == "" || cc.Detail == "" {
		t.Errorf("missing = %+v", cc)
	}
}

// A failed check forgets nothing: the release found before stays, the Claude
// Code version learnt before stays for the same installed Claude Code, and
// the errors come along.
func TestUpdatesHostFailedCheckKeepsWhatItKnew(t *testing.T) {
	f := newHostFixture(t, "v0.4.0")
	f.checkAndWait(t)
	first := f.h.View().CheckedAt
	f.mu.Lock()
	f.tag = ""
	f.cc = ccversion.Status{Checked: true, Installed: &ccversion.Installed{Path: "/x/claude", Version: "2.1.0", Method: ccversion.Native}, Err: errors.New("HTTP 502")}
	f.mu.Unlock()
	f.checkAndWait(t)
	v := f.h.View()
	if !v.Available || !v.App.Available || v.App.Latest != "v0.5.0" || !strings.Contains(v.App.Error, "503") {
		t.Errorf("app after a failed check = %+v", v.App)
	}
	if !v.ClaudeCode.Available || v.ClaudeCode.Latest != "2.2.0" || v.ClaudeCode.Error != "HTTP 502" || v.ClaudeCode.State != "update" {
		t.Errorf("claudeCode after a failed check = %+v", v.ClaudeCode)
	}
	if v.CheckedAt == nil || !v.CheckedAt.Equal(*first) {
		t.Errorf("checkedAt moved although nothing was reached: %v vs %v", v.CheckedAt, first)
	}
	// A different Claude Code (updated by hand meanwhile) does not inherit
	// the old newest version: the state is unknown with the error.
	f.mu.Lock()
	f.cc = ccversion.Status{Checked: true, Installed: &ccversion.Installed{Path: "/x/claude", Version: "2.3.0", Method: ccversion.Native}, Err: errors.New("HTTP 502")}
	f.mu.Unlock()
	f.checkAndWait(t)
	if cc := f.h.View().ClaudeCode; cc.State != "unknown" || cc.Available || !strings.Contains(cc.Detail, "HTTP 502") {
		t.Errorf("another Claude Code = %+v", cc)
	}
}

func TestUpdatesHostApplyApp(t *testing.T) {
	f := newHostFixture(t, "v0.4.0")
	// Nothing known yet: refused.
	if _, err := f.h.Apply("app"); err == nil {
		t.Fatal("apply before a check: want an error")
	}
	f.checkAndWait(t)
	// The upgrade prints guidance and fails: the output is the answer.
	f.mu.Lock()
	f.upCode, f.upOut = 1, "tycswap was built from a checkout: git pull && make install\n"
	f.mu.Unlock()
	res, err := f.h.Apply("app")
	if err == nil || !strings.Contains(err.Error(), "built from a checkout") || !strings.Contains(res.Output, "git pull") {
		t.Fatalf("failed upgrade: %+v, %v", res, err)
	}
	if f.h.View().App.Installed {
		t.Error("a failed upgrade was recorded as installed")
	}
	// It works: the card says installed; restart.
	f.mu.Lock()
	f.upCode, f.upOut = 0, "go: downloading ...\n"
	f.mu.Unlock()
	res, err = f.h.Apply("app")
	if err != nil || !strings.Contains(res.Message, "0.5.0 is installed") || !strings.Contains(res.Message, "web again") || res.Output != "go: downloading ..." {
		t.Fatalf("upgrade: %+v, %v", res, err)
	}
	v := f.h.View()
	if v.App.Available || !v.App.Installed {
		t.Errorf("app after install = %+v", v.App)
	}
	if _, err := f.h.Apply("plugins"); err == nil {
		t.Error("unknown target accepted")
	}
}

func TestUpdatesHostApplyClaudeCode(t *testing.T) {
	f := newHostFixture(t, "v0.5.0")
	if _, err := f.h.Apply("claude-code"); err == nil || !strings.Contains(err.Error(), "not been checked") {
		t.Fatalf("apply before a check: %v", err)
	}
	f.checkAndWait(t)
	// The installer changes the version, and Apply verifies it immediately.
	res, err := f.h.Apply("claude-code")
	if err != nil || !strings.Contains(res.Message, "Claude Code is updated") || res.Output != "updated" {
		t.Fatalf("apply: %+v, %v", res, err)
	}
	f.mu.Lock()
	ran := append([]string(nil), f.ranCmds...)
	f.mu.Unlock()
	if len(ran) != 1 || ran[0] != "claude update" {
		t.Errorf("ran %v", ran)
	}
	if cc := f.h.View().ClaudeCode; cc.State != "latest" {
		t.Errorf("after the update: %+v", cc)
	}
	if _, err := f.h.Apply("claude-code"); err == nil || !strings.Contains(err.Error(), "no update") {
		t.Errorf("apply with nothing to do: %v", err)
	}
	// The npm install runs npm, not claude.
	f.mu.Lock()
	f.cc = ccversion.Status{Checked: true, Installed: &ccversion.Installed{Path: "/x/claude", Version: "2.1.0", Method: ccversion.NPM}, Latest: "2.2.0"}
	f.mu.Unlock()
	f.checkAndWait(t)
	if _, err := f.h.Apply("claude-code"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	last := f.ranCmds[len(f.ranCmds)-1]
	f.mu.Unlock()
	if !strings.HasPrefix(last, "npm install -g") {
		t.Errorf("npm install ran %q", last)
	}
}

func TestUpdatesHostApplyClaudeCodeVerifiesAdvertisedVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{"no-op", "2.1.0", "still at 2.1.0 (expected 2.2.0 or newer)"},
		{"partial update", "2.1.5", "still at 2.1.5 (expected 2.2.0 or newer)"},
		{"invalid version", "01.2.0", "still at 01.2.0 (expected 2.2.0 or newer)"},
		{"version unavailable", "", "could not be verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHostFixture(t, "v0.5.0")
			f.h.runClaude = func(context.Context, ccversion.Command, *ccversion.Installed) (string, error) {
				f.mu.Lock()
				defer f.mu.Unlock()
				installed := *f.cc.Installed
				installed.Version = tc.version
				f.cc.Installed = &installed
				return "Installer finished\n", nil
			}
			f.checkAndWait(t)
			res, err := f.h.Apply("claude-code")
			if err == nil || !strings.Contains(err.Error(), tc.want) || res.Message != "" || res.Output != "Installer finished" {
				t.Fatalf("Apply = %+v, %v", res, err)
			}
			if got := f.h.View().ClaudeCode.Installed; got != tc.version {
				t.Errorf("installed version = %q, want %q", got, tc.version)
			}
		})
	}
}

func TestUpdatesHostDoesNotReuseLatestAcrossChannels(t *testing.T) {
	f := newHostFixture(t, "v0.5.0")
	f.cc.Installed.Channel = "latest"
	f.checkAndWait(t)
	f.mu.Lock()
	installed := *f.cc.Installed
	installed.Channel = "stable"
	f.cc = ccversion.Status{Checked: true, Installed: &installed, Err: errors.New("stable endpoint unavailable")}
	f.mu.Unlock()
	f.checkAndWait(t)
	if cc := f.h.View().ClaudeCode; cc.Available || cc.Latest != "" || cc.State != "unknown" {
		t.Fatalf("stable inherited latest's cached release: %+v", cc)
	}
}

// A failing Claude Code command is the error's last line, with the output.
func TestUpdatesHostApplyClaudeCodeFails(t *testing.T) {
	f := newHostFixture(t, "v0.5.0")
	f.h.runClaude = func(context.Context, ccversion.Command, *ccversion.Installed) (string, error) {
		return "Checking...\nError: no permission to write\n", errors.New("exit status 1")
	}
	f.checkAndWait(t)
	res, err := f.h.Apply("claude-code")
	if err == nil || err.Error() != "Error: no permission to write" || !strings.Contains(res.Output, "Checking...") {
		t.Errorf("failed apply: %+v, %v", res, err)
	}
}

func TestUpgradeHintAndViewHelpers(t *testing.T) {
	if !newerRelease("v0.5.0", "v0.4.0") || newerRelease("v0.4.0", "v0.4.0") || newerRelease("junk", "v0.4.0") {
		t.Error("newerRelease")
	}
	if got := lastOutputLine("a\n\nb\n\n", "x"); got != "b" {
		t.Errorf("lastOutputLine = %q", got)
	}
	if got := lastOutputLine("  \n", "fallback"); got != "fallback" {
		t.Errorf("lastOutputLine empty = %q", got)
	}
	// This test binary is a checkout build, so the hint is the checkout's.
	if got := upgradeHint("/anywhere/tycswap", 0); got != "git pull && make install" {
		t.Errorf("upgradeHint = %q", got)
	}
	// The button for a binary SelfUpgrade upgrades itself (go install off
	// Windows, or the release downloaded over it), the line to type for a
	// go-installed binary on Windows, the package manager for its binary, and
	// the releases page for one that can be neither — never `go install` for
	// a release binary.
	goInstall := "go install " + update.ModulePath + "@latest"
	releases := "download it from " + update.ReleasesURL
	for _, c := range []struct {
		plan update.Plan
		plat platform.Platform
		want string
	}{
		{update.Plan{Method: update.MethodCheckout}, platform.Linux, "git pull && make install"},
		{update.Plan{Method: update.MethodGoInstall}, platform.Linux, ""},
		{update.Plan{Method: update.MethodGoInstall}, platform.MacOS, ""},
		{update.Plan{Method: update.MethodGoInstall}, platform.Windows, goInstall},
		{update.Plan{Method: update.MethodDownload}, platform.Linux, ""},
		{update.Plan{Method: update.MethodDownload}, platform.Windows, ""},
		{update.Plan{Method: update.MethodPackageManager, Manager: "Nix"}, platform.Linux, "Nix (it is in the Nix store)"},
		{update.Plan{Method: update.MethodPackageManager}, platform.MacOS, "the package manager that installed it"},
		{update.Plan{Method: update.MethodGoInstallElsewhere}, platform.Linux, goInstall},
		{update.Plan{Method: update.MethodGoInstallElsewhere}, platform.Windows, goInstall},
		{update.Plan{Method: update.MethodManual}, platform.Linux, releases},
		{update.Plan{Method: update.MethodManual}, platform.Windows, releases},
	} {
		if got := methodHint(c.plan, c.plat); got != c.want {
			t.Errorf("methodHint(%+v, %v) = %q, want %q", c.plan, c.plat, got, c.want)
		}
	}
	v := claudeCodeView(ccversion.Status{Checked: true, Installed: &ccversion.Installed{Version: "", Method: ccversion.Unknown}})
	if v.State != "unknown" || !strings.Contains(v.Error, "installed version is unknown") {
		t.Errorf("unknown version view = %+v", v)
	}
}

// The plan reads the build and the real environment, as SelfUpgrade does: a
// module build in $HOME/go/bin is a go install and one elsewhere is told the
// `go install` line, a release build in a
// writable directory gets the next release downloaded over it, one in the
// Nix store is the package manager's, and a checkout build is the checkout's
// wherever it is.
func TestUpgradePlanReadsTheEnvironment(t *testing.T) {
	home := t.TempDir()
	testutil.Setenv(t, "HOME", home)
	testutil.Setenv(t, "USERPROFILE", home)
	testutil.Unsetenv(t, "GOPATH")
	testutil.Unsetenv(t, "GOBIN")
	prev := buildSource
	t.Cleanup(func() { buildSource = prev })
	writable := filepath.Join(t.TempDir(), "tycswap")
	if err := os.WriteFile(writable, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		src  update.BuildSource
		exe  string
		want update.Method
	}{
		{update.SourceModule, filepath.Join(home, "go", "bin", "tycswap"), update.MethodGoInstall},
		{update.SourceModule, writable, update.MethodGoInstallElsewhere},
		{update.SourceRelease, writable, update.MethodDownload},
		{update.SourceRelease, "/nix/store/0000-tycswap/bin/tycswap", update.MethodPackageManager},
		{update.SourceCheckout, writable, update.MethodCheckout},
	} {
		buildSource = func() update.BuildSource { return c.src }
		if got := upgradePlan(c.exe); got.Method != c.want {
			t.Errorf("%v at %s: %+v, want method %v", c.src, c.exe, got, c.want)
		}
	}
	// A module build outside a Go bin directory: the card and the tray name
	// the `go install` line `tycswap upgrade` prints for it, not the
	// releases page.
	buildSource = func() update.BuildSource { return update.SourceModule }
	if got, want := upgradeHint(writable, platform.Linux), "go install "+update.ModulePath+"@latest"; got != want {
		t.Errorf("module build outside a Go bin directory: hint %q, want %q", got, want)
	}
}
