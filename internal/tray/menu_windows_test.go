package tray

import (
	"testing"
	"unsafe"
)

// Exercise native menu state and GDI output: active rows must remain inert
// while their text is rendered in the shared accent even when disabled.
func TestActiveMenuAccent(t *testing.T) {
	items := []Item{{ID: "active", Title: "Active account", Kind: KindGauge, Checked: true, Disabled: true}}
	plan := menuPlan(items)
	menu := buildMenu(plan)
	if menu == 0 {
		t.Fatal("CreatePopupMenu failed")
	}
	defer pDestroyMenu.Call(menu)
	state, _, _ := user32.NewProc("GetMenuState").Call(menu, plan[0].cmd, 0)
	if state&mfGrayed == 0 || state&mfOwnerDraw == 0 {
		t.Fatalf("native menu flags: %#x", state)
	}
	var model menuModel
	model.set(items)
	if _, ok := model.idAt(0); ok {
		t.Fatal("active account became clickable")
	}
	p := newActiveMenuPainter(plan)
	defer p.close()
	var font menuLogFont
	got, _, _ := menuGDI.NewProc("GetObjectW").Call(p.font, unsafe.Sizeof(font), uintptr(unsafe.Pointer(&font)))
	if got == 0 || font.weight < 700 {
		t.Fatalf("active account font is not bold: %d", font.weight)
	}
	m := measureMenuItem{ctlType: odtMenu, itemData: plan[0].cmd}
	if !p.measure(0, &m) || m.itemWidth < 50 || m.itemHeight < 10 {
		t.Fatalf("invalid measurement: %+v", m)
	}
	dc, _, _ := menuGetDC.Call(0)
	defer menuReleaseDC.Call(0, dc)
	mem, _, _ := menuGDI.NewProc("CreateCompatibleDC").Call(dc)
	if mem == 0 {
		t.Fatal("CreateCompatibleDC failed")
	}
	defer menuGDI.NewProc("DeleteDC").Call(mem)
	bmp, _, _ := menuGDI.NewProc("CreateCompatibleBitmap").Call(dc, uintptr(m.itemWidth), uintptr(m.itemHeight))
	if bmp == 0 {
		t.Fatal("CreateCompatibleBitmap failed")
	}
	defer menuDeleteObject.Call(bmp)
	old, _, _ := menuSelectObject.Call(mem, bmp)
	defer menuSelectObject.Call(mem, old)
	d := drawMenuItem{ctlType: odtMenu, itemData: plan[0].cmd, itemState: 4, hdc: mem, rect: menuRect{right: int32(m.itemWidth), bottom: int32(m.itemHeight)}}
	if !p.draw(&d) {
		t.Fatal("disabled active row was not painted")
	}
	pixel := menuGDI.NewProc("GetPixel")
	blue := 0
	for y := uintptr(0); y < uintptr(m.itemHeight); y++ {
		for x := uintptr(menuGutter()); x < uintptr(m.itemWidth); x++ {
			color, _, _ := pixel.Call(mem, x, y)
			if color == menuAccentColor() {
				blue++
			}
		}
	}
	if blue == 0 {
		t.Fatal("account text has no accent pixels")
	}
	// The label remains available to accessibility despite custom painting.
	buf := make([]uint16, 512)
	info := menuItemInfo{mask: 0x40, text: &buf[0], textLength: uint32(len(buf))}
	info.size = uint32(unsafe.Sizeof(info))
	ok, _, _ := user32.NewProc("GetMenuItemInfoW").Call(menu, plan[0].cmd, 0, uintptr(unsafe.Pointer(&info)))
	if ok == 0 || info.textLength == 0 {
		t.Fatal("accessible menu label missing")
	}
}
