//go:build windows

package tray

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tyclab/tycswap/internal/brand"
)

// Win32 shell notification icon, driven through a hidden top-level window and
// a classic GetMessage loop. Everything the shell needs (Shell_NotifyIconW,
// popup menus, icon creation from PNG) is reached by syscall; no cgo. The
// window must stay top-level, not message-only: only top-level windows get
// the "TaskbarCreated" broadcast that says Explorer restarted and the icon
// has to be added again (DESIGN A52).

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	pAppendMenuW              = user32.NewProc("AppendMenuW")
	pDestroyMenu              = user32.NewProc("DestroyMenu")
	pTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	pDestroyIcon              = user32.NewProc("DestroyIcon")
	pShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
)

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmCommand     = 0x0111
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B
	wmApp         = 0x8000
	wmTrayIcon    = wmApp + 1
	wmTrayUpdate  = wmApp + 2
	wmTrayNotify  = wmApp + 3
	wmTrayQuit    = wmApp + 4
	wmTraySetIcon = wmApp + 5

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10
	niifInfo   = 0x01

	mfString    = 0x0000
	mfSeparator = 0x0800
	mfChecked   = 0x0008
	mfGrayed    = 0x0001
	mfPopup     = 0x0010

	tpmReturnCmd   = 0x0100
	tpmNonotify    = 0x0080
	tpmBottomAlign = 0x0020
	tpmRightAlign  = 0x0008

	lrDefaultColor = 0
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type point struct{ x, y int32 }

type msg struct {
	hwnd    windows.HWND
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             windows.HWND
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     windows.Handle
}

type windowsTray struct {
	icon Icon
	opts Options

	mu       sync.Mutex
	menu     menuModel
	tooltip  string
	pendingN [2]string
	iconPNG  []byte // the notification-area icon: New's PNG until SetIcon

	hwnd    windows.HWND       // set by Run under mu; read under mu off the UI thread
	quit    bool               // under mu: Quit was called
	hicon   windows.Handle     // UI thread only
	painter *activeMenuPainter // UI thread only, bound to the open menu snapshot
	ready   chan struct{}
	err     error
}

var (
	winMu   sync.Mutex
	winTray *windowsTray
)

// wmTaskbarCreated is the message Explorer broadcasts to every top-level
// window when the taskbar is (re)created, after an Explorer crash or restart:
// every notification icon is gone then and must be added again. Registered by
// Run before the window exists; 0 until then, and when the registration
// failed, so it never matches.
var wmTaskbarCreated uint32

// shellNotifyIcon is Shell_NotifyIconW, nil on success and the call's error
// otherwise; a var so a test can see what the tray asks the shell for.
var shellNotifyIcon = func(op uintptr, nid *notifyIconData) error {
	if ok, _, err := pShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(nid))); ok == 0 {
		return err
	}
	return nil
}

// registerTaskbarCreated registers the "TaskbarCreated" message into
// wmTaskbarCreated (unchanged when the registration fails).
func registerTaskbarCreated() {
	if m, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16z("TaskbarCreated")))); m != 0 {
		wmTaskbarCreated = uint32(m)
	}
}

func newTray(icon Icon, opts Options) (Tray, error) {
	if len(icon.PNG) == 0 {
		return nil, ErrUnsupported
	}
	t := &windowsTray{icon: icon, opts: opts, tooltip: opts.Tooltip, iconPNG: icon.PNG, ready: make(chan struct{})}
	winMu.Lock()
	winTray = t
	winMu.Unlock()
	return t, nil
}

func utf16z(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	n := copy(dst, u)
	if n == len(dst) {
		dst[len(dst)-1] = 0
	}
}

// windowClass names the hidden window's class after the program:
// "TycswapTray" for tycswap.
func windowClass() string {
	n := brand.Sanitized().Name
	return strings.ToUpper(n[:1]) + n[1:] + "Tray"
}

func (t *windowsTray) Run() error {
	// Win32 binds a window, and the queue that PostMessageW fills for it, to
	// the OS thread that created it, and GetMessageW(hwnd=0) only reads the
	// calling thread's queue. Go does not pin the main goroutine after init:
	// when the blocking GetMessageW returns while every P is busy, or when the
	// loop is preempted, the goroutine resumes on another thread and from then
	// on polls an empty queue. Icon clicks, Quit and every SetTooltip, Notify
	// and SetIcon (the update badge, DESIGN A44) would then go unanswered.
	// So the goroutine keeps this one thread from RegisterClassExW through
	// the message loop to NIM_DELETE, until Run returns.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className := utf16z(windowClass())
	hInst, _, _ := pGetModuleHandleW.Call(0)
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   windows.NewCallback(trayWndProc),
		hInstance:     windows.Handle(hInst),
		lpszClassName: className,
	}
	if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return errors.New("tray: RegisterClassEx: " + err.Error())
	}
	registerTaskbarCreated()
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16z(brand.Sanitized().Name))),
		0, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		return errors.New("tray: CreateWindowEx: " + err.Error())
	}
	// Set under mu, where Quit reads it: a Quit from before the window is
	// posted here and a later one posts itself, so none is lost or posted
	// twice (A52).
	t.mu.Lock()
	t.hwnd = windows.HWND(hwnd)
	quit, png := t.quit, t.iconPNG
	t.mu.Unlock()
	if quit {
		pPostMessageW.Call(hwnd, wmTrayQuit, 0, 0)
	}
	hicon, err := iconFromPNG(png)
	if err != nil {
		return err
	}
	t.hicon = hicon

	if err := t.addIcon(); err != nil {
		return errors.New("tray: Shell_NotifyIcon(NIM_ADD): " + err.Error())
	}

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	del := t.baseNID()
	shellNotifyIcon(nimDelete, &del)
	pDestroyIcon.Call(uintptr(t.hicon))
	return t.err
}

// addIcon puts the icon in the notification area with t.hicon, the current
// tooltip and the click callback, at start and on readdIcon. UI thread only.
func (t *windowsTray) addIcon() error {
	t.mu.Lock()
	tip := t.tooltip
	t.mu.Unlock()
	nid := t.baseNID()
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = wmTrayIcon
	nid.hIcon = t.hicon
	copyUTF16(nid.szTip[:], tip)
	return shellNotifyIcon(nimAdd, &nid)
}

// readdIcon answers wmTaskbarCreated: Explorer recreated the taskbar and the
// icon is gone. The HICON is built again from the latest PNG asked for, since
// a SetIcon while Explorer was down failed its NIM_MODIFY and kept the older
// icon (the update badge would otherwise stay missing); when that build fails
// the icon there was is added. The old HICON goes only after a successful
// add, as in applyIcon: any process can broadcast the message, so the shell
// may still be showing it. A failed add keeps the old HICON and frees the
// new one, which the shell never took; it is not retried. UI thread only.
func (t *windowsTray) readdIcon() {
	t.mu.Lock()
	png := t.iconPNG
	t.mu.Unlock()
	old := t.hicon
	if hicon, err := iconFromPNG(png); err == nil {
		t.hicon = hicon
	}
	if t.addIcon() != nil {
		old, t.hicon = t.hicon, old // free the new one below, keep the old
	}
	if old != 0 && old != t.hicon {
		pDestroyIcon.Call(uintptr(old))
	}
}

// iconFromPNG builds an HICON: CreateIconFromResourceEx understands PNG
// payloads since Vista. The caller owns it (DestroyIcon).
func iconFromPNG(png []byte) (windows.Handle, error) {
	if len(png) == 0 {
		return 0, errors.New("tray: empty icon") // &png[0] below needs a byte
	}
	hicon, _, err := pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)), 1, 0x00030000, 0, 0, lrDefaultColor)
	if hicon == 0 {
		return 0, errors.New("tray: CreateIconFromResourceEx: " + err.Error())
	}
	return windows.Handle(hicon), nil
}

func (t *windowsTray) baseNID() notifyIconData {
	return notifyIconData{cbSize: uint32(unsafe.Sizeof(notifyIconData{})), hWnd: t.hwnd, uID: 1}
}

// Quit before Run has its window is kept, and Run posts it then: the app's
// SIGINT claim turns a Ctrl-C into a Quit before Run starts (DESIGN A48, A52).
func (t *windowsTray) Quit() {
	t.mu.Lock()
	t.quit = true
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(uintptr(hwnd), wmTrayQuit, 0, 0)
	}
}

// SetTitle has no text slot on the Windows taskbar; it becomes the tooltip.
func (t *windowsTray) SetTitle(s string) { t.SetTooltip(s) }

func (t *windowsTray) SetTooltip(s string) {
	t.mu.Lock()
	t.tooltip = s
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(uintptr(hwnd), wmTrayUpdate, 0, 0)
	}
}

// SetIcon stores the PNG and has the UI thread swap the icon (applyIcon).
// Before Run there is no window yet; Run then starts with this icon.
func (t *windowsTray) SetIcon(icon Icon) {
	if len(icon.PNG) == 0 {
		return
	}
	t.mu.Lock()
	t.iconPNG = icon.PNG
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(uintptr(hwnd), wmTraySetIcon, 0, 0)
	}
}

func (t *windowsTray) SetMenu(items []Item) {
	t.mu.Lock()
	t.menu.set(items)
	t.mu.Unlock()
}

func (t *windowsTray) Notify(title, body string) error {
	t.mu.Lock()
	t.pendingN = [2]string{title, body}
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd == 0 {
		return errors.New("tray: not running")
	}
	pPostMessageW.Call(uintptr(hwnd), wmTrayNotify, 0, 0)
	return nil
}

// Ask shows a Yes/No message box (the labels are fixed by Windows).
func (t *windowsTray) Ask(title, body, okLabel, cancelLabel string) (bool, error) {
	const mbYesNo, mbIconQuestion, mbSetForeground, mbTopmost = 0x4, 0x20, 0x10000, 0x40000
	const idYes = 6
	r, _, err := pMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16z(body+"\n\n"+okLabel+" = Yes, "+cancelLabel+" = No"))), uintptr(unsafe.Pointer(utf16z(title))), mbYesNo|mbIconQuestion|mbSetForeground|mbTopmost)
	if r == 0 {
		return false, errors.New("tray: MessageBox: " + err.Error())
	}
	return r == idYes, nil
}

// UI-thread work, reached through posted messages.

func (t *windowsTray) applyTooltip() {
	t.mu.Lock()
	tip := t.tooltip
	t.mu.Unlock()
	nid := t.baseNID()
	nid.uFlags = nifTip
	copyUTF16(nid.szTip[:], tip)
	shellNotifyIcon(nimModify, &nid)
}

// applyIcon hands the shell a new HICON (NIM_MODIFY) and only then destroys
// the one it replaces, which the shell may still be drawing until then. A
// failure keeps the icon that is there.
func (t *windowsTray) applyIcon() {
	t.mu.Lock()
	png := t.iconPNG
	t.mu.Unlock()
	hicon, err := iconFromPNG(png)
	if err != nil {
		return
	}
	nid := t.baseNID()
	nid.uFlags = nifIcon
	nid.hIcon = hicon
	if shellNotifyIcon(nimModify, &nid) != nil {
		pDestroyIcon.Call(uintptr(hicon))
		return
	}
	old := t.hicon
	t.hicon = hicon
	pDestroyIcon.Call(uintptr(old))
}

func (t *windowsTray) showNotification() {
	t.mu.Lock()
	title, body := t.pendingN[0], t.pendingN[1]
	t.mu.Unlock()
	nid := t.baseNID()
	nid.uFlags = nifInfo
	nid.dwInfoFlags = niifInfo
	copyUTF16(nid.szInfoTitle[:], title)
	copyUTF16(nid.szInfo[:], body)
	shellNotifyIcon(nimModify, &nid)
}

// menuStep is one instruction for building the popup menu: append a row or
// a separator to the menu being built, open a submenu (later rows go into
// it), or close it and attach it to its parent under title.
type menuStep struct {
	op    menuOp
	flags uintptr
	cmd   uintptr // command ID: the menuModel tag + 1 (0 means "no choice")
	title string
}

type menuOp int

const (
	opRow menuOp = iota
	opSeparator
	opOpen
	opClose
)

// menuPlan turns items into the build steps, numbered by walkMenu so that a
// command ID minus one is the tag idAt takes. Kept free of syscalls so the
// layout can be tested off Windows' message loop.
func menuPlan(items []Item) []menuStep {
	var plan []menuStep
	var open []menuStep // the submenus being built, innermost last
	walkMenu(items, func(tag int, it Item) {
		if it.Separator {
			plan = append(plan, menuStep{op: opSeparator, flags: mfSeparator})
			return
		}
		flags := uintptr(mfString)
		if it.Kind == KindGauge && it.Checked {
			flags |= mfOwnerDraw
		}
		if it.Checked {
			flags |= mfChecked
		}
		if it.Disabled || it.Kind == KindHeader {
			flags |= mfGrayed
		}
		plan = append(plan, menuStep{op: opRow, flags: flags, cmd: uintptr(tag + 1), title: it.FallbackTitle()})
	}, func(_ int, it Item) {
		flags := uintptr(mfString | mfPopup)
		if it.Disabled {
			flags |= mfGrayed
		}
		step := menuStep{op: opOpen, flags: flags, title: it.Title}
		open = append(open, step)
		plan = append(plan, step)
	}, func() {
		step := open[len(open)-1]
		open = open[:len(open)-1]
		step.op = opClose
		plan = append(plan, step)
	})
	return plan
}

// buildMenu runs plan against Win32 and returns the root HMENU. A submenu is
// attached to its parent (MF_POPUP) only once it is complete; from then on
// DestroyMenu on the root frees it too. On failure everything built so far,
// attached or not, is destroyed and 0 comes back.
func buildMenu(plan []menuStep) uintptr {
	root, _, _ := pCreatePopupMenu.Call()
	if root == 0 {
		return 0
	}
	stack := []uintptr{root}
	fail := func() uintptr {
		for i := len(stack) - 1; i >= 0; i-- { // unattached submenus, then the root
			pDestroyMenu.Call(stack[i])
		}
		return 0
	}
	for _, st := range plan {
		cur := stack[len(stack)-1]
		switch st.op {
		case opSeparator:
			pAppendMenuW.Call(cur, st.flags, 0, 0)
		case opRow:
			if st.flags&mfOwnerDraw != 0 {
				pAppendMenuW.Call(cur, st.flags, st.cmd, st.cmd)
				setOwnerDrawTitle(cur, st.cmd, st.title)
			} else {
				pAppendMenuW.Call(cur, st.flags, st.cmd, uintptr(unsafe.Pointer(utf16z(st.title))))
			}
		case opOpen:
			sub, _, _ := pCreatePopupMenu.Call()
			if sub == 0 {
				return fail()
			}
			stack = append(stack, sub)
		case opClose:
			stack = stack[:len(stack)-1]
			parent := stack[len(stack)-1]
			if ok, _, _ := pAppendMenuW.Call(parent, st.flags, cur, uintptr(unsafe.Pointer(utf16z(st.title)))); ok == 0 {
				pDestroyMenu.Call(cur) // not attached, so the root would not free it
				return fail()
			}
		}
	}
	return root
}

// showMenu builds the menu at popup time from a snapshot of the model and
// resolves the choice against that same snapshot, so a SetMenu while the
// menu is open cannot map the command ID onto a different row.
func (t *windowsTray) showMenu() {
	t.mu.Lock()
	snap := t.menu // set replaces items and flat, never mutates them
	t.mu.Unlock()
	plan := menuPlan(snap.items)
	t.painter = newActiveMenuPainter(plan)
	defer func() { t.painter.close(); t.painter = nil }()
	hmenu := buildMenu(plan)
	if hmenu == 0 {
		return
	}
	defer pDestroyMenu.Call(hmenu) // frees the attached submenus as well
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(uintptr(t.hwnd)) // otherwise the menu does not dismiss on outside clicks
	cmd, _, _ := pTrackPopupMenu.Call(hmenu, tpmReturnCmd|tpmNonotify|tpmBottomAlign|tpmRightAlign, uintptr(pt.x), uintptr(pt.y), 0, uintptr(t.hwnd), 0)
	if cmd == 0 {
		return
	}
	id, ok := snap.idAt(int(cmd) - 1)
	if ok && t.opts.OnClick != nil {
		go t.opts.OnClick(id)
	}
}

func trayWndProc(hwnd windows.HWND, message uint32, wParam, lParam uintptr) uintptr {
	winMu.Lock()
	t := winTray
	winMu.Unlock()
	if t == nil {
		r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
		return r
	}
	// Explorer restarted: the icon it had is gone. Not a constant, so not a
	// case of the switch below.
	if wmTaskbarCreated != 0 && message == wmTaskbarCreated {
		t.readdIcon()
		return 0
	}
	switch message {
	case wmMeasureItem:
		if lParam != 0 && t.painter.measure(uintptr(hwnd), *(**measureMenuItem)(unsafe.Pointer(&lParam))) {
			return 1
		}
	case wmDrawItem:
		if lParam != 0 && t.painter.draw(*(**drawMenuItem)(unsafe.Pointer(&lParam))) {
			return 1
		}
	case wmTrayIcon:
		switch uint32(lParam & 0xffff) {
		case wmLButtonUp, wmRButtonUp, wmContextMenu:
			t.showMenu()
		}
		return 0
	case wmTrayUpdate:
		t.applyTooltip()
		return 0
	case wmTrayNotify:
		t.showNotification()
		return 0
	case wmTraySetIcon:
		t.applyIcon()
		return 0
	case wmTrayQuit:
		pDestroyWindow.Call(uintptr(hwnd))
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return r
}
