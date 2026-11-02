//go:build windows

package tray

import (
	"fmt"
	"golang.org/x/sys/windows"
)

var sessionDLL = windows.NewLazySystemDLL("wtsapi32.dll")
var registerWTS = sessionDLL.NewProc("WTSRegisterSessionNotification")
var unregisterWTS = sessionDLL.NewProc("WTSUnRegisterSessionNotification")

func registerSessionEvents(hwnd uintptr) error   { return callWTS(registerWTS, hwnd, 0) }
func unregisterSessionEvents(hwnd uintptr) error { return callWTS(unregisterWTS, hwnd) }

// callWTS looks the export up first: LazyProc.Call panics when wtsapi32.dll or the export is missing.
func callWTS(proc *windows.LazyProc, args ...uintptr) error {
	if err := proc.Find(); err != nil {
		return fmt.Errorf("tray: session notifications unavailable: %w", err)
	}
	if ok, _, err := proc.Call(args...); ok == 0 {
		return fmt.Errorf("tray: %s: %w", proc.Name, err)
	}
	return nil
}

const (
	wmPowerBroadcast   = 0x0218
	wmWTSSessionChange = 0x02b1
	wmDisplayChange    = 0x007e
)

// sessionEvents names WM_WTSSESSION_CHANGE's wParam, WTS_CONSOLE_CONNECT (1) to WTS_SESSION_REMOTE_CONTROL (9).
var sessionEvents = [...]string{1: EventConsoleConnect, 2: EventConsoleDisconnect, 3: EventRemoteConnect, 4: EventRemoteDisconnect,
	5: EventLogon, 6: EventLogoff, 7: EventLock, 8: EventUnlock, 9: EventRemoteControl}

func systemEvent(message uint32, value uintptr) string {
	switch message {
	case wmPowerBroadcast:
		switch value {
		case 4: // PBT_APMSUSPEND
			return EventSuspend
		case 0x12: // PBT_APMRESUMEAUTOMATIC: every wake
			return EventResumeAutomatic
		case 7: // PBT_APMRESUMESUSPEND: the user is present
			return EventResume
		}
	case wmWTSSessionChange:
		if value < uintptr(len(sessionEvents)) {
			return sessionEvents[value]
		}
	case wmDisplayChange:
		return EventDisplayChange
	}
	return ""
}
