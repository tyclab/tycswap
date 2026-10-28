//go:build windows

package tray

import (
	"fmt"
	"golang.org/x/sys/windows"
)

var sessionDLL = windows.NewLazySystemDLL("wtsapi32.dll")
var registerWTS = sessionDLL.NewProc("WTSRegisterSessionNotification")
var unregisterWTS = sessionDLL.NewProc("WTSUnRegisterSessionNotification")

func registerSessionEvents(hwnd uintptr) error {
	if ok, _, err := registerWTS.Call(hwnd, 0); ok == 0 {
		return fmt.Errorf("tray: session notifications unavailable: %w", err)
	}
	return nil
}
func unregisterSessionEvents(hwnd uintptr) { unregisterWTS.Call(hwnd) }
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
