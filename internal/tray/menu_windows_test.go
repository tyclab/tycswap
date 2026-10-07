package tray

import (
	"testing"
	"unsafe"
)

// Exercise native menu state and GDI output: active rows must remain inert
// while their text is rendered in the shared accent even when disabled.
func TestActiveMenuAccent(t *testing.T) {
	items := []Item{{ID: "active", Title: "Active account", Kind: KindPlain, Active: true, Disabled: true}}
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

func TestDarkMenuRenderingAndNavigation(t *testing.T) {
	items := []Item{{ID: "open", Title: "Open dashboard"}, {ID: "active", Title: "Active account", Active: true, Disabled: true}, Header("Accounts"), Separator(), {ID: "toggle", Kind: KindToggle, Title: "Model limits", Checked: true}, {Title: "More", Children: []Item{{ID: "child", Title: "Child"}}}}
	plan := themedMenuPlan(menuPlan(items), menuDark)
	p := newMenuPainter(plan, menuDark)
	defer p.close()
	if p.rowFont(plan[1]) != p.font || p.rowFont(plan[4]) == p.font {
		t.Fatal("active account and checked toggle must use different font weights")
	}
	menu := buildMenu(plan)
	if menu == 0 {
		t.Fatal("menu build failed")
	}
	defer pDestroyMenu.Call(menu)
	setMenuBackground(menu, p.background)
	info := popupMenuInfo{mask: 2}
	info.size = uint32(unsafe.Sizeof(info))
	ok, _, _ := user32.NewProc("GetMenuInfo").Call(menu, uintptr(unsafe.Pointer(&info)))
	if ok == 0 || info.background != p.background {
		t.Fatal("native menu background not themed")
	}
	if got := p.menuChar(menu, 'o'); got != 2<<16 {
		t.Fatalf("Open keyboard action: %#x", got)
	}
	if got := p.menuChar(menu, 'a'); got != 0 {
		t.Fatalf("disabled account/header executable: %#x", got)
	}
	dc, _, _ := menuGetDC.Call(0)
	defer menuReleaseDC.Call(0, dc)
	mem, _, _ := menuGDI.NewProc("CreateCompatibleDC").Call(dc)
	defer menuGDI.NewProc("DeleteDC").Call(mem)
	bmp, _, _ := menuGDI.NewProc("CreateCompatibleBitmap").Call(dc, 800, 60)
	defer menuDeleteObject.Call(bmp)
	old, _, _ := menuSelectObject.Call(mem, bmp)
	defer menuSelectObject.Call(mem, old)
	for _, step := range plan {
		if step.op == opOpen {
			continue
		}
		m := measureMenuItem{ctlType: odtMenu, itemData: step.cmd}
		if !p.measure(0, &m) || m.itemHeight == 0 {
			t.Fatalf("unmeasured %+v", step)
		}
		for _, selected := range []uint32{0, 1} {
			d := drawMenuItem{ctlType: odtMenu, itemData: step.cmd, itemState: selected, hdc: mem, rect: menuRect{right: 800, bottom: 60}}
			if !p.draw(&d) {
				t.Fatalf("unpainted %+v", step)
			}
			pixel, _, _ := menuGDI.NewProc("GetPixel").Call(mem, 1, 1)
			want := uintptr(0x202020)
			if selected != 0 && step.flags&mfGrayed == 0 {
				want = 0x383838
			}
			if pixel != want {
				t.Fatalf("row %s background %#x want %#x", step.title, pixel, want)
			}
		}
	}
	for _, step := range themedMenuPlan(menuPlan(items), menuHighContrast) {
		if step.flags&mfOwnerDraw != 0 {
			t.Fatal("high contrast must use native colors")
		}
	}
}

func TestMenuActiveAccountIndependentOfKindAndToggle(t *testing.T) {
	items := []Item{
		{ID: "default", Title: "Default", Active: true, Disabled: true},
		{Title: "Fable", Children: []Item{{ID: "group:fable:1", Title: "Group account", Active: true, Disabled: true}}},
		{ID: "gauge", Title: "Gauge", Kind: KindGauge, Pct: -1, Active: true, Disabled: true},
		{ID: "auto", Title: "Auto-switch", Kind: KindToggle, Checked: true},
		{ID: "checked", Title: "Checked", Checked: true},
	}
	for _, theme := range []menuTheme{menuLight, menuDark, menuHighContrast} {
		plan := themedMenuPlan(menuPlan(items), theme)
		for _, step := range plan {
			if step.op != opRow {
				continue
			}
			account := step.title == "Default" || step.title == "Group account" || step.title == "Gauge"
			if step.active != account || step.flags&mfChecked == 0 {
				t.Fatalf("theme %d row %s lost distinct active/check state", theme, step.title)
			}
			if account && step.flags&mfGrayed == 0 {
				t.Fatalf("theme %d active account became executable", theme)
			}
			ownerDraw := theme == menuDark || theme == menuLight && account
			if (step.flags&mfOwnerDraw != 0) != ownerDraw {
				t.Fatalf("theme %d row %s owner drawing mismatch", theme, step.title)
			}
		}
	}
}

func TestDeviceThemeChangeClosesStaleMenu(t *testing.T) {
	oldReader, oldEnd, oldTray := readMenuTheme, endThemedMenu, winTray
	defer func() { readMenuTheme, endThemedMenu, winTray = oldReader, oldEnd, oldTray }()
	current := menuLight
	readMenuTheme = func() menuTheme { return current }
	closed := 0
	endThemedMenu = func() { closed++ }
	winTray = &windowsTray{painter: &activeMenuPainter{theme: menuLight}}
	trayWndProc(0, 0x001a, 0, 0)
	if closed != 0 {
		t.Fatal("unchanged theme closed menu")
	}
	current = menuDark
	trayWndProc(0, 0x001a, 0, 0)
	if closed != 1 {
		t.Fatal("open menu kept stale theme")
	}
	winTray.painter = nil
	trayWndProc(0, 0x001a, 0, 0)
	if closed != 1 {
		t.Fatal("closed menu should not be touched")
	}
}
