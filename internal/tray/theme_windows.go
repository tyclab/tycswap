package tray

import (
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

type menuTheme uint8

const (
	menuLight menuTheme = iota
	menuDark
	menuHighContrast
)

var readMenuTheme = windowsMenuTheme
var endThemedMenu = func() { user32.NewProc("EndMenu").Call() }

func windowsMenuTheme() menuTheme {
	var hc struct {
		size, flags uint32
		scheme      uintptr
	}
	hc.size = uint32(unsafe.Sizeof(hc))
	if ok, _, _ := menuSystemParameters.Call(0x42, uintptr(hc.size), uintptr(unsafe.Pointer(&hc)), 0); ok != 0 && hc.flags&1 != 0 {
		return menuHighContrast
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return menuLight
	}
	defer key.Close()
	light, _, err := key.GetIntegerValue("AppsUseLightTheme")
	if err == nil && light == 0 {
		return menuDark
	}
	return menuLight
}

func themedMenuPlan(plan []menuStep, theme menuTheme) []menuStep {
	result := append([]menuStep(nil), plan...)
	for i := range result {
		step := &result[i]
		if theme == menuHighContrast {
			step.flags &^= mfOwnerDraw
			continue
		}
		if theme != menuDark {
			continue
		}
		if step.op == opOpen {
			continue
		}
		if step.cmd == 0 {
			step.cmd = uintptr(0x10000 + i)
		}
		step.flags |= mfOwnerDraw
	}
	return result
}

type popupMenuInfo struct {
	size, mask, style, maxHeight uint32
	background                   uintptr
	helpID                       uint32
	data                         uintptr
}

func setMenuBackground(menu, brush uintptr) {
	info := popupMenuInfo{mask: 0x80000002, background: brush}
	info.size = uint32(unsafe.Sizeof(info))
	user32.NewProc("SetMenuInfo").Call(menu, uintptr(unsafe.Pointer(&info)))
}
