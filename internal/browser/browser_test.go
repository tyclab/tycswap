package browser

import (
	"errors"
	"reflect"
	"strings"
	"testing"
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

// Every platform's launcher argv, checked on every platform.
func TestBrowserCommand(t *testing.T) {
	u := "http://127.0.0.1:1/?a=1&b=2"
	name, args := browserCommand("darwin", u)
	if name != "open" || !reflect.DeepEqual(args, []string{u}) {
		t.Errorf("darwin: %s %v", name, args)
	}
	name, args = browserCommand("windows", u)
	if name != "cmd" || !reflect.DeepEqual(args, []string{"/c", "start", "", "http://127.0.0.1:1/?a=1^&b=2"}) {
		t.Errorf("windows: %s %v", name, args)
	}
	name, args = browserCommand("linux", u)
	if name != "sh" || len(args) != 2 || args[0] != "-c" {
		t.Fatalf("linux: %s %v", name, args)
	}
	for _, want := range []string{`wslview "` + u + `"`, `xdg-open "` + u + `"`, `sensible-browser "` + u + `"`} {
		if !strings.Contains(args[1], want) {
			t.Errorf("linux script %q lacks %q", args[1], want)
		}
	}
	if strings.Index(args[1], "wslview") > strings.Index(args[1], "xdg-open") ||
		strings.Index(args[1], "xdg-open") > strings.Index(args[1], "sensible-browser") {
		t.Errorf("linux chain out of order: %q", args[1])
	}
}

// Anything that could break out of the double-quoted sh string, or that cmd
// would expand, is refused before a launcher is ever started.
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
