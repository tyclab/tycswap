package tray

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A submenu becomes an open/close pair around its children, the closing
// step carrying MF_POPUP and the label; every leaf's command ID minus one
// resolves through idAt to that leaf, also inside the submenu.
func TestMenuPlanNestsSubmenusAndNumbersByTag(t *testing.T) {
	items := []Item{
		{ID: "open", Title: "Open"},
		{ID: "more", Title: "More", Children: []Item{
			{ID: "a", Title: "A", Checked: true},
			Separator(),
			{ID: "b", Title: "B", Disabled: true},
		}},
		{ID: "quit", Title: "Quit"},
	}
	got := menuPlan(items)
	want := []menuStep{
		{op: opRow, flags: mfString, cmd: 1, title: "Open"},
		{op: opOpen, flags: mfString | mfPopup, title: "More"},
		{op: opRow, flags: mfString | mfChecked, cmd: 3, title: "A"},
		{op: opSeparator, flags: mfSeparator},
		{op: opRow, flags: mfString | mfGrayed, cmd: 5, title: "B"},
		{op: opClose, flags: mfString | mfPopup, title: "More"},
		{op: opRow, flags: mfString, cmd: 6, title: "Quit"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan =\n%+v\nwant\n%+v", got, want)
	}

	var m menuModel
	m.set(items)
	for cmd, id := range map[uintptr]string{1: "open", 3: "a", 6: "quit"} {
		if got, ok := m.idAt(int(cmd) - 1); !ok || got != id {
			t.Errorf("command %d = %q,%v, want %q", cmd, got, ok, id)
		}
	}
	if _, ok := m.idAt(1); ok {
		t.Error("the submenu row itself must not resolve to an ID")
	}
}

// When Explorer restarts it broadcasts "TaskbarCreated" and every icon is
// gone; the tray adds its icon again, with the current tooltip and click
// callback and an icon built from the latest PNG asked for, since a SetIcon
// while Explorer was down kept the older icon (DESIGN A52). Before, the icon
// stayed away until the app restarted. Other messages add nothing.
func TestTaskbarCreatedAddsTheIconAgain(t *testing.T) {
	prevShell, prevMsg := shellNotifyIcon, wmTaskbarCreated
	registerTaskbarCreated() // what Run does before it creates the window
	if wmTaskbarCreated == 0 {
		t.Fatal("RegisterWindowMessage(TaskbarCreated) failed")
	}
	type call struct {
		op       uintptr
		flags    uint32
		callback uint32
		icon     windows.Handle
		tip      string
	}
	var calls []call
	shellNotifyIcon = func(op uintptr, nid *notifyIconData) error {
		calls = append(calls, call{op, nid.uFlags, nid.uCallbackMessage, nid.hIcon, windows.UTF16ToString(nid.szTip[:])})
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	// No HICON yet, so an add that reused the old one would carry 0.
	tr := &windowsTray{tooltip: "first", iconPNG: buf.Bytes()}
	winMu.Lock()
	prevTray := winTray
	winTray = tr
	winMu.Unlock()
	t.Cleanup(func() {
		shellNotifyIcon, wmTaskbarCreated = prevShell, prevMsg
		winMu.Lock()
		winTray = prevTray
		winMu.Unlock()
		if tr.hicon != 0 {
			pDestroyIcon.Call(uintptr(tr.hicon))
		}
	})

	tr.mu.Lock()
	tr.tooltip = "Account-2 · 5h 41%"
	tr.mu.Unlock()
	if r := trayWndProc(0, wmTaskbarCreated, 0, 0); r != 0 {
		t.Errorf("TaskbarCreated returned %d, want 0", r)
	}
	if tr.hicon == 0 {
		t.Fatal("no icon was built from the PNG")
	}
	want := call{nimAdd, nifMessage | nifIcon | nifTip, wmTrayIcon, tr.hicon, "Account-2 · 5h 41%"}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("shell calls = %+v, want one %+v", calls, want)
	}

	// The tray's own messages are not a re-add.
	calls = nil
	trayWndProc(0, wmTrayUpdate, 0, 0)
	for _, c := range calls {
		if c.op == nimAdd {
			t.Errorf("a tooltip update added the icon: %+v", calls)
		}
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A Quit before Run has its window ends Run. The app, the headless app and
// the remote tray turn a Ctrl-C into a Quit from their SIGINT claim on, which
// is before Run (DESIGN A48, A52); that Quit used to be dropped, and the tray
// kept running.
func TestQuitBeforeRunEndsRun(t *testing.T) {
	prevShell := shellNotifyIcon
	shellNotifyIcon = func(uintptr, *notifyIconData) error { return nil } // no icon in the real taskbar
	winMu.Lock()
	prevTray := winTray
	winMu.Unlock()
	t.Cleanup(func() {
		shellNotifyIcon = prevShell
		winMu.Lock()
		winTray = prevTray
		winMu.Unlock()
	})
	tr, err := newTray(Icon{PNG: testPNG(t)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tr.Quit()
	done := make(chan error, 1)
	go func() { done <- tr.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a Quit that came before it")
	}
	// Run registered the window class; free it so the next Run in this
	// process (-count=2) can register it again.
	hInst, _, _ := pGetModuleHandleW.Call(0)
	unregister := user32.NewProc("UnregisterClassW")
	if ok, _, err := unregister.Call(uintptr(unsafe.Pointer(utf16z(windowClass()))), hInst); ok == 0 {
		t.Errorf("UnregisterClass: %v", err)
	}
}

// A re-add the shell refuses keeps the icon there was, which the shell may
// still be showing (any process can broadcast TaskbarCreated), and frees the
// new one the shell never took (DESIGN A52). The shown icon used to be
// destroyed regardless.
func TestFailedReaddKeepsTheShownIcon(t *testing.T) {
	prevShell := shellNotifyIcon
	var offered windows.Handle
	shellNotifyIcon = func(_ uintptr, nid *notifyIconData) error {
		offered = nid.hIcon
		return errors.New("refused")
	}
	t.Cleanup(func() { shellNotifyIcon = prevShell })
	png := testPNG(t)
	shown, err := iconFromPNG(png)
	if err != nil {
		t.Fatal(err)
	}
	tr := &windowsTray{iconPNG: png, hicon: shown}
	tr.readdIcon()
	if tr.hicon != shown {
		t.Errorf("current icon = %#x, want the shown %#x", tr.hicon, shown)
	}
	if ok, _, _ := pDestroyIcon.Call(uintptr(shown)); ok == 0 {
		t.Error("the shown icon was destroyed")
	}
	if offered == 0 || offered == shown {
		t.Fatalf("the re-add offered %#x, want a new icon", offered)
	}
	if ok, _, _ := pDestroyIcon.Call(uintptr(offered)); ok != 0 {
		t.Error("the icon the shell refused was not freed")
	}
}
