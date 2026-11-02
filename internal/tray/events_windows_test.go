//go:build windows

package tray

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPowerAndSessionEventsRemainDistinct(t *testing.T) {
	for _, v := range []struct {
		message uint32
		value   uintptr
		want    string
	}{
		{0x218, 4, "suspend"}, {0x218, 18, "resume-automatic"}, {0x218, 7, "resume"}, {0x218, 10, ""},
		{0x2b1, 1, "console-connect"}, {0x2b1, 2, "console-disconnect"}, {0x2b1, 3, "remote-connect"},
		{0x2b1, 4, "remote-disconnect"}, {0x2b1, 5, "logon"}, {0x2b1, 6, "logoff"}, {0x2b1, 7, "lock"},
		{0x2b1, 8, "unlock"}, {0x2b1, 9, "remote-control"}, {0x2b1, 10, ""}, {0x7e, 0, "display-change"},
	} {
		if got := systemEvent(v.message, v.value); got != v.want {
			t.Errorf("%x/%d: %q, want %q", v.message, v.value, got, v.want)
		}
	}
}

func TestSystemEventsReachOnlyOptedInTray(t *testing.T) {
	winMu.Lock()
	old := winTray
	seen := ""
	winTray = &windowsTray{opts: Options{OnSystemEvent: func(s string) { seen = s }}}
	winMu.Unlock()
	t.Cleanup(func() { winMu.Lock(); winTray = old; winMu.Unlock() })
	trayWndProc(0, 0x218, 18, 0)
	if seen != "resume-automatic" {
		t.Fatal("automatic wake was lost or treated as user resume")
	}
}

func TestMissingSessionExportIsAnError(t *testing.T) {
	prevRegister, prevUnregister := registerWTS, unregisterWTS
	t.Cleanup(func() { registerWTS, unregisterWTS = prevRegister, prevUnregister })
	for _, proc := range []*windows.LazyProc{sessionDLL.NewProc("TycswapNoSuchExport"), windows.NewLazySystemDLL("tycswap-no-such.dll").NewProc("WTSRegisterSessionNotification")} {
		registerWTS, unregisterWTS = proc, proc
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s panicked: %v", proc.Name, r)
				}
			}()
			if err := registerSessionEvents(0); err == nil {
				t.Errorf("register through a missing %s succeeded", proc.Name)
			}
			if err := unregisterSessionEvents(0); err == nil {
				t.Errorf("unregister through a missing %s succeeded", proc.Name)
			}
		}()
	}
}

var pIsWindow = user32.NewProc("IsWindow")

// stubSessionEvents stands in for wtsapi32 and the shell for one test.
func stubSessionEvents(t *testing.T, register, unregister func(uintptr) error) {
	prevRegister, prevUnregister, prevShell := registerSessionEvents, unregisterSessionEvents, shellNotifyIcon
	registerSessionEvents, unregisterSessionEvents = register, unregister
	shellNotifyIcon = func(uintptr, *notifyIconData) error { return nil }
	winMu.Lock()
	prevTray := winTray
	winMu.Unlock()
	t.Cleanup(func() {
		registerSessionEvents, unregisterSessionEvents, shellNotifyIcon = prevRegister, prevUnregister, prevShell
		winMu.Lock()
		winTray = prevTray
		winMu.Unlock()
	})
}

// runTestTray starts Run and waits for its window; the cleanup quits it.
func runTestTray(t *testing.T, opts Options) (*windowsTray, uintptr, chan error) {
	t.Helper()
	opts.Identifier = "TycswapEventsTest"
	tr, err := newTray(Icon{PNG: testPNG(t)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	w := tr.(*windowsTray)
	done := make(chan error, 1)
	go func() { done <- w.Run() }()
	t.Cleanup(func() {
		w.Quit()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not end after Quit")
		}
	})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("Run returned before its window was up: %v", err)
		default:
		}
		w.mu.Lock()
		hwnd := w.hwnd
		w.mu.Unlock()
		if hwnd != 0 {
			return w, uintptr(hwnd), done
		}
	}
	t.Fatal("Run created no window")
	return nil, 0, nil
}

func TestSessionEventsUnregisterWhileTheWindowExists(t *testing.T) {
	var registered, unregistered, alive uintptr
	stubSessionEvents(t, func(hwnd uintptr) error { registered = hwnd; return nil }, func(hwnd uintptr) error {
		unregistered = hwnd
		alive, _, _ = pIsWindow.Call(hwnd)
		return nil
	})
	tr, hwnd, done := runTestTray(t, Options{OnSystemEvent: func(string) {}})
	tr.Quit()
	select {
	case err := <-done:
		done <- err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end after Quit")
	}
	if registered != hwnd || unregistered != hwnd || alive == 0 {
		t.Fatalf("registered %#x, unregistered %#x (window alive then: %v), want both %#x while it exists", registered, unregistered, alive != 0, hwnd)
	}
}

// Registration can fail, e.g. RPC_S_INVALID_BINDING before Remote Desktop Services is up at logon:
// the tray keeps running with power events and says session events are off.
func TestFailedSessionRegistrationKeepsPowerEvents(t *testing.T) {
	unregistered := 0
	stubSessionEvents(t, func(uintptr) error { return errors.New("RPC_S_INVALID_BINDING") }, func(uintptr) error { unregistered++; return nil })
	events := make(chan string, 4)
	tr, hwnd, done := runTestTray(t, Options{OnSystemEvent: func(e string) { events <- e }})
	pPostMessageW.Call(hwnd, wmPowerBroadcast, 4, 0)
	for _, want := range []string{"session-events-unavailable", "suspend"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("event %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %q event", want)
		}
	}
	tr.Quit()
	select {
	case err := <-done:
		done <- err
		if err != nil {
			t.Fatalf("Run = %v, want nil after a degraded start", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end after Quit")
	}
	if unregistered != 0 {
		t.Errorf("unregistered %d times a subscription that never existed", unregistered)
	}
}

// Only WM_POWERBROADCAST wants TRUE; without a handler the messages are DefWindowProc's.
func TestSystemMessagesAnswerAsDocumented(t *testing.T) {
	winMu.Lock()
	old := winTray
	winMu.Unlock()
	t.Cleanup(func() { winMu.Lock(); winTray = old; winMu.Unlock() })
	use := func(tr *windowsTray) { winMu.Lock(); winTray = tr; winMu.Unlock() }
	use(&windowsTray{opts: Options{OnSystemEvent: func(string) {}}})
	for message, want := range map[uint32]uintptr{wmPowerBroadcast: 1, wmWTSSessionChange: 0, wmDisplayChange: 0} {
		if got := trayWndProc(0, message, 7, 0); got != want {
			t.Errorf("handled %#x returned %d, want %d", message, got, want)
		}
	}
	use(&windowsTray{})
	for _, message := range []uint32{wmPowerBroadcast, wmWTSSessionChange, wmDisplayChange} {
		want, _, _ := pDefWindowProcW.Call(0, uintptr(message), 7, 0)
		if got := trayWndProc(0, message, 7, 0); got != want {
			t.Errorf("unhandled %#x returned %d, want DefWindowProc's %d", message, got, want)
		}
	}
}
