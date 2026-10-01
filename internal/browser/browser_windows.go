// browser_windows.go — open a URL in the default browser on Windows.
//
// Three ways, in order, each a fallback for the one before:
//
//  1. ShellExecuteW(NULL, "open", url) — what Explorer does with a link. No
//     child process, no console window, and no command line for anything to
//     re-parse, so nothing in the URL can be split or expanded.
//  2. rundll32.exe url.dll,FileProtocolHandler <url> — the classic spawned
//     equivalent, for a shell that refuses the call.
//  3. cmd.exe /c start "" <url> — last, because cmd.exe has its own quoting
//     and %-expansion rules.
//
// Every executable is named by its full path in the system directory, so a
// PATH that happens to hold another cmd.exe is never consulted.

//go:build windows

package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procShellExecuteW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")

// shellExecuteWait bounds the wait for ShellExecuteW. It normally returns in
// milliseconds; one that has not returned by then is still handing the URL
// over (a cold browser start, a slow shell extension), and falling through to
// the next way would open a second tab — which, for a single-use dashboard
// token, is a tab that fails.
const shellExecuteWait = 20 * time.Second

func detach(cmd *exec.Cmd) {
	// CREATE_NO_WINDOW: a console child of a GUI process would otherwise get
	// a console window of its own for the moment it runs.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

// openURL is Open on Windows.
func openURL(url string) error {
	return openChain(url, []opener{
		{name: "ShellExecuteW", open: shellExecuteOpen},
		{name: "rundll32", open: rundll32Open},
		{name: "cmd start", open: cmdStartOpen},
	})
}

func shellExecuteOpen(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		// ShellExecute may hand the URL to a shell extension through COM, and
		// its documentation asks for COM on the calling thread (apartment
		// threaded, OLE1 DDE off). A goroutine is not bound to a thread, so
		// pin it for the call.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		switch hr := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); hr {
		case nil, syscall.Errno(1): // S_OK, S_FALSE: both must be balanced
			defer windows.CoUninitialize()
		}
		r, _, _ := procShellExecuteW.Call(0,
			uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)),
			0, 0, uintptr(windows.SW_SHOWNORMAL))
		done <- shellExecuteError(r)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(shellExecuteWait):
		return nil
	}
}

// shellExecuteError turns ShellExecuteW's return value into an error; above
// 32 is success. The codes are the SE_ERR_* and system values it documents.
func shellExecuteError(r uintptr) error {
	if r > 32 {
		return nil
	}
	why := map[uintptr]string{
		0:  "out of memory or resources",
		2:  "file not found",
		3:  "path not found",
		5:  "access denied",
		8:  "out of memory",
		11: "invalid executable",
		26: "sharing violation",
		27: "the association for web links is incomplete",
		28: "timed out",
		29: "DDE transaction failed",
		30: "DDE busy",
		31: "no program is associated with web links",
		32: "DLL not found",
	}[r]
	if why == "" {
		why = "error"
	}
	return fmt.Errorf("returned %d (%s)", r, why)
}

func rundll32Open(url string) error {
	cmd := exec.Command(systemPath("rundll32.exe"), "url.dll,FileProtocolHandler", url)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func cmdStartOpen(url string) error {
	cmd := exec.Command(systemPath("cmd.exe"), cmdStartArgs(url)...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// systemPath is name in the Windows system directory.
func systemPath(name string) string {
	if dir, err := windows.GetSystemDirectory(); err == nil && dir != "" {
		return filepath.Join(dir, name)
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", name)
	}
	return name
}
