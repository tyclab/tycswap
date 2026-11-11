//go:build windows

package tray

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type panelBitmapInfo struct {
	size                       uint32
	width, height              int32
	planes, bitCount           uint16
	compression, image         uint32
	xPerMeter, yPerMeter       int32
	colorsUsed, colorsRequired uint32
}

// PanelPicture draws p as the tray shows it and returns the panel window's
// pixels, for the preview tool. The window sits outside every monitor, is
// never activated, and no tray icon is added. theme is "dark", "light" or
// "" for the system's.
func PanelPicture(p Panel, theme string) (*image.RGBA, error) {
	forced := map[string]menuTheme{"dark": menuDark, "light": menuLight}
	if _, ok := forced[theme]; !ok && theme != "" {
		return nil, fmt.Errorf("tray: theme %q is not dark or light", theme)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	setPanelDPIAware()
	t := &windowsTray{panelData: clonePanel(p), panelSet: true}
	winMu.Lock()
	if winTray != nil {
		winMu.Unlock()
		return nil, errors.New("tray: this process already has a tray")
	}
	winTray = t
	winMu.Unlock()
	defer func() {
		winMu.Lock()
		winTray = nil
		winMu.Unlock()
	}()
	if theme != "" {
		previous := readMenuTheme
		readMenuTheme = func() menuTheme { return forced[theme] }
		defer func() { readMenuTheme = previous }()
	}
	if err := t.newPanel(true); err != nil {
		return nil, err
	}
	panel := t.panel
	defer panel.close()
	panel.update()
	panelShow.Call(panel.hwnd, 4) // SW_SHOWNOACTIVATE, still outside every monitor
	user32.NewProc("RedrawWindow").Call(panel.hwnd, 0, 0, 0x1|0x4|0x100|0x80)
	panelSettle()
	picture, err := panel.capture(2) // PW_RENDERFULLCONTENT: what the compositor shows
	if err == nil && panelBlank(picture) {
		picture, err = panel.capture(0)
	}
	panelShow.Call(panel.hwnd, 0)
	return picture, err
}

// panelSettle runs this thread's messages for a moment, so the popup and
// its buttons have painted and the compositor has their frame.
func panelSettle() {
	flush := windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmFlush")
	var m msg
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); {
		for {
			if ok, _, _ := user32.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1); ok == 0 {
				break
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		if flush.Find() == nil {
			flush.Call()
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (p *windowsPanel) capture(flags uintptr) (*image.RGBA, error) {
	var window menuRect
	user32.NewProc("GetWindowRect").Call(p.hwnd, uintptr(unsafe.Pointer(&window)))
	width, height := int(window.right-window.left), int(window.bottom-window.top)
	if width <= 0 || height <= 0 {
		return nil, errors.New("tray: the panel has no size")
	}
	screen, _, _ := menuGetDC.Call(0)
	defer menuReleaseDC.Call(0, screen)
	memory, _, _ := menuGDI.NewProc("CreateCompatibleDC").Call(screen)
	defer menuGDI.NewProc("DeleteDC").Call(memory)
	info := panelBitmapInfo{width: int32(width), height: -int32(height), planes: 1, bitCount: 32}
	info.size = uint32(unsafe.Sizeof(info))
	var bits unsafe.Pointer
	bitmap, _, err := menuGDI.NewProc("CreateDIBSection").Call(screen, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return nil, fmt.Errorf("tray: CreateDIBSection: %w", err)
	}
	defer menuDeleteObject.Call(bitmap)
	old, _, _ := menuSelectObject.Call(memory, bitmap)
	defer menuSelectObject.Call(memory, old)
	if ok, _, err := user32.NewProc("PrintWindow").Call(p.hwnd, memory, flags); ok == 0 {
		return nil, fmt.Errorf("tray: PrintWindow: %w", err)
	}
	menuGDI.NewProc("GdiFlush").Call()
	source := unsafe.Slice((*byte)(bits), width*height*4)
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(source); i += 4 {
		picture.Pix[i], picture.Pix[i+1], picture.Pix[i+2], picture.Pix[i+3] = source[i+2], source[i+1], source[i], 255
	}
	return picture, nil
}

func panelBlank(picture *image.RGBA) bool {
	for i := 4; i < len(picture.Pix); i += 4 {
		if picture.Pix[i] != picture.Pix[0] || picture.Pix[i+1] != picture.Pix[1] || picture.Pix[i+2] != picture.Pix[2] {
			return false
		}
	}
	return true
}
