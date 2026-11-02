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
func systemEvent(message uint32, value uintptr) string {
	switch message {
	case 0x0218:
		switch value {
		case 4:
			return "suspend"
		case 18:
			return "resume-automatic"
		case 7:
			return "resume"
		}
	case 0x02b1:
		switch value {
		case 2, 4, 6, 7:
			return "suspend"
		case 1, 3, 8:
			return "resume"
		}
	case 0x007e:
		return "display-change"
	}
	return ""
}
