package browser

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The first way that works ends the chain; later ones never run (a second
// one would open a second tab, which for a single-use token is a failing one).
func TestOpenChainStopsAtTheFirstSuccess(t *testing.T) {
	var ran []string
	step := func(name string, err error) opener {
		return opener{name: name, open: func(u string) error {
			ran = append(ran, name+"("+u+")")
			return err
		}}
	}
	err := openChain("http://127.0.0.1:1/", []opener{
		step("ShellExecuteW", errors.New("returned 31 (no program is associated with web links)")),
		step("rundll32", nil),
		step("cmd start", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ShellExecuteW(http://127.0.0.1:1/)", "rundll32(http://127.0.0.1:1/)"}; !reflect.DeepEqual(ran, want) {
		t.Errorf("ran %v, want %v", ran, want)
	}
}

// When every way fails, the error names each one and why.
func TestOpenChainReportsEveryAttempt(t *testing.T) {
	err := openChain("http://x/", []opener{
		{name: "ShellExecuteW", open: func(string) error { return errors.New("returned 2 (file not found)") }},
		{name: "rundll32", open: func(string) error { return errors.New("exec: not found") }},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"ShellExecuteW: returned 2 (file not found)", "rundll32: exec: not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if openChain("http://x/", nil) == nil {
		t.Error("an empty chain must fail")
	}
}

// The URLs the dashboard hands over pass the launcher's check; a raw Windows
// path glued to "file://" does not.
func TestDashboardURLsPassTheCheck(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1:52117/?token=launch-token-for-tests",
		"file:///var/folders/x/T/tycswap-dashboard-1.html",
		"file:///tmp/a%20b/c.html",
		"file:///C:/Users/USER~1/AppData/Local/Temp/tycswap-dashboard-1.html",
	} {
		if !urlShellSafe(u) {
			t.Errorf("urlShellSafe(%q) = false", u)
		}
	}
	if urlShellSafe(`file://C:\Users\a\AppData\Local\Temp\tycswap-dashboard-1.html`) {
		t.Error("a raw Windows path must still be refused")
	}
}

// Every platform's launcher chain, checked on every platform: wslview first
// so a WSL distro reaches the Windows browser, `open` alone on macOS, and the
// cmd.exe fallback's caret escaping.
func TestLaunchers(t *testing.T) {
	if got := launchers("darwin"); !reflect.DeepEqual(got, []string{"open"}) {
		t.Errorf("darwin: %v", got)
	}
	for _, goos := range []string{"linux", "freebsd"} {
		if got := launchers(goos); !reflect.DeepEqual(got, []string{"wslview", "xdg-open", "sensible-browser"}) {
			t.Errorf("%s: %v", goos, got)
		}
	}
	if got := cmdStartArgs("http://127.0.0.1:1/?a=1&b=2"); !reflect.DeepEqual(got, []string{"/c", "start", "", "http://127.0.0.1:1/?a=1^&b=2"}) {
		t.Errorf("cmd start args: %v", got)
	}
	names := func(steps []opener) []string {
		var out []string
		for _, s := range steps {
			out = append(out, s.name)
		}
		return out
	}
	if got := names(launcherOpeners("linux")); !reflect.DeepEqual(got, launchers("linux")) {
		t.Errorf("opener names %v, want %v", got, launchers("linux"))
	}
}

// No launcher on PATH: the chain fails, naming every binary it looked for,
// instead of reporting success for a shell that found nothing.
func TestLauncherChainFailsWhenNothingIsOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := openChain("http://127.0.0.1:1/", launcherOpeners("linux"))
	if err == nil {
		t.Fatal("an empty PATH opened a browser")
	}
	for _, bin := range launchers("linux") {
		if !strings.Contains(err.Error(), bin+": ") {
			t.Errorf("error %q does not name %s", err, bin)
		}
	}
}

// The first launcher on PATH is started directly with the URL as its only
// argument; the ones before it, absent from PATH, are skipped.
func TestLauncherChainStartsTheFirstBinaryFound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix launcher chain")
	}
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argv + "\n"
	if err := os.WriteFile(filepath.Join(dir, "xdg-open"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	u := "http://127.0.0.1:1/?a=1&b=2"
	if err := openChain(u, launcherOpeners("linux")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(argv)
		if err == nil && strings.TrimSpace(string(b)) == u {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("launcher argv = %q (%v), want %q", b, err, u)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Anything a command interpreter would re-parse (the cmd.exe fallback) or
// that is outside RFC 3986 is refused before a launcher is ever started.
func TestOpenRefusesShellSignificantURLs(t *testing.T) {
	for _, u := range []string{
		"",
		"http://x/$(id)",
		"http://x/`id`",
		"http://x/'a'",
		"http://x/!1",
		`http://x/"; id; "`,
		"http://x/a b",
		"http://x/\n",
		"http://x/\\",
		"http://x/%ok|pipe",
	} {
		if urlShellSafe(u) {
			t.Errorf("urlShellSafe(%q) = true", u)
		}
		if err := Open(u); err == nil {
			t.Errorf("Open(%q) succeeded", u)
		}
	}
}
