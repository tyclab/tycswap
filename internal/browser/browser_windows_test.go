//go:build windows

package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func TestShellExecuteError(t *testing.T) {
	if err := shellExecuteError(42); err != nil {
		t.Errorf("42 (> 32) is success, got %v", err)
	}
	if err := shellExecuteError(31); err == nil || !strings.Contains(err.Error(), "no program is associated") {
		t.Errorf("31 = %v", err)
	}
	if err := shellExecuteError(17); err == nil || !strings.Contains(err.Error(), "returned 17") {
		t.Errorf("17 = %v", err)
	}
}

// The launchers are named by full path, never looked up on PATH.
func TestSystemPathIsAbsolute(t *testing.T) {
	for _, name := range []string{"cmd.exe", "rundll32.exe"} {
		p := systemPath(name)
		if !filepath.IsAbs(p) || !strings.EqualFold(filepath.Base(p), name) || !strings.EqualFold(filepath.Base(filepath.Dir(p)), "System32") {
			t.Errorf("systemPath(%q) = %q", name, p)
		}
	}
}

// openTestEnv gates TestOpenersReallyLaunch: it registers a URL scheme for the
// current user, which a developer's machine should not get as a side effect of
// `go test`. Set it on a Windows desktop to run the test.
const openTestEnv = "TYCSWAP_WINDOWS_OPEN_TEST"

var procSHChangeNotify = windows.NewLazySystemDLL("shell32.dll").NewProc("SHChangeNotify")

// Every way of opening a link really hands it to its registered handler — the
// same path an http link takes to the default browser, with a throwaway scheme
// whose handler writes the URL to a file instead of starting a browser.
func TestOpenersReallyLaunch(t *testing.T) {
	if os.Getenv(openTestEnv) != "1" {
		t.Skip("registers a throwaway URL scheme for the current user; set " + openTestEnv + "=1")
	}
	const scheme = "tycswaptest"
	marker := filepath.Join(t.TempDir(), "opened.txt")

	// Windows only launches URL handlers for a process on the interactive
	// desktop. Hosted CI runners run the job in session 0, a service window
	// station, where ShellExecuteW answers "access denied" for a URL while it
	// still starts a plain program (both recorded below). A user's browser is
	// opened from the interactive desktop, so that is where this test must pass;
	// elsewhere it records what the session is and why it cannot run.
	station := windowStation()
	var session uint32
	_ = windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session)
	exeErr := shellExecuteFile(systemPath("cmd.exe"), `/c echo exe>>"`+marker+`"`)
	t.Logf("window station %q, session %d; ShellExecuteW on cmd.exe itself: %v", station, session, exeErr)
	if !strings.EqualFold(station, "WinSta0") {
		t.Skipf("no interactive desktop (window station %q): Windows refuses URL launches here", station)
	}
	base := `Software\Classes\` + scheme
	t.Cleanup(func() {
		for _, sub := range []string{`\shell\open\command`, `\shell\open`, `\shell`, ``} {
			_ = registry.DeleteKey(registry.CURRENT_USER, base+sub)
		}
	})
	root, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.SetStringValue("", "URL:tycswap test"); err != nil {
		t.Fatal(err)
	}
	if err := root.SetStringValue("URL Protocol", ""); err != nil {
		t.Fatal(err)
	}
	root.Close()
	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	// %1 is the URL; each opener appends its own line.
	if err := cmd.SetStringValue("", `"`+systemPath("cmd.exe")+`" /c echo %1>>"`+marker+`"`); err != nil {
		t.Fatal(err)
	}
	cmd.Close()
	procSHChangeNotify.Call(0x08000000, 0, 0, 0) // SHCNE_ASSOCCHANGED, SHCNF_IDLIST

	for _, o := range []opener{
		{name: "shellexecute", open: shellExecuteOpen},
		{name: "rundll32", open: rundll32Open},
		{name: "cmd-start", open: cmdStartOpen},
		{name: "default", open: Open},
	} {
		u := scheme + "://" + o.name
		if err := o.open(u); err != nil {
			t.Errorf("%s: %v", o.name, err)
			continue
		}
		if !waitForLine(marker, u, 30*time.Second) {
			raw, _ := os.ReadFile(marker)
			t.Errorf("%s: the handler never ran for %s (marker: %q)", o.name, u, raw)
		}
	}
}

var (
	procGetProcessWindowStation   = windows.NewLazySystemDLL("user32.dll").NewProc("GetProcessWindowStation")
	procGetUserObjectInformationW = windows.NewLazySystemDLL("user32.dll").NewProc("GetUserObjectInformationW")
)

// windowStation names this process's window station: "WinSta0" on the
// interactive desktop, "Service-0x0-…$" in a service or remote session.
func windowStation() string {
	h, _, _ := procGetProcessWindowStation.Call()
	if h == 0 {
		return ""
	}
	var buf [256]uint16
	var need uint32
	const uoiName = 2
	r, _, _ := procGetUserObjectInformationW.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}

// shellExecuteFile is ShellExecuteW "open" on a program with arguments.
func shellExecuteFile(file, args string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	f, _ := windows.UTF16PtrFromString(file)
	a, _ := windows.UTF16PtrFromString(args)
	r, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(a)), 0, 0)
	return shellExecuteError(r)
}

func waitForLine(path, want string, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && strings.Contains(string(raw), want) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
