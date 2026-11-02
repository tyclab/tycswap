//go:build windows

package tray

import (
	"testing"

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
