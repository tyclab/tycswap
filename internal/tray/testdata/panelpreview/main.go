//go:build windows

// panelpreview shows the tray panel with a synthetic fixture. With -png it
// draws the panel outside every monitor, without focus or a tray icon,
// writes the picture and exits; without it, it runs a tray icon whose click
// opens the panel.
//
// The picture checks drawing only. The fixture is a tray.Panel written by
// hand, not one the app builds from a state, so the builder's rules (dashes,
// clock texts, limit colours) are the cli tests' to check. The window takes
// the DPI of the monitor nearest the left of the virtual screen, so on a
// desktop whose monitors differ in DPI the picture's scale can differ from
// the real panel's, which opens on the tray's monitor.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/tyclab/tycswap/internal/tray"
	"golang.org/x/sys/windows"
)

func main() {
	picture := flag.String("png", "", "write a PNG of the panel window to this path and exit")
	theme := flag.String("theme", "", "dark or light for -png; the system's when empty")
	flag.Parse()
	model := fixture(time.Now())
	if *picture == "" {
		interactive(model)
		return
	}
	img, err := tray.PanelPicture(model, *theme)
	if err == nil {
		var out bytes.Buffer
		if err = png.Encode(&out, img); err == nil {
			err = os.WriteFile(*picture, out.Bytes(), 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// fixture is the approved picture: reset times count from now, so the
// picture never goes stale. Its texts follow the app's panel builder.
func fixture(now time.Time) tray.Panel {
	type limit struct {
		pct   float64
		in    time.Duration // 0: the window has not started
		dash  bool
		limit float64
	}
	cell := func(l limit, fiveHour bool) tray.PanelCell {
		if l.dash {
			return tray.PanelCell{Absent: true}
		}
		c := tray.PanelCell{Share: fmt.Sprintf("%.0f%%", l.pct), Pct: l.pct, Limit: l.pct >= l.limit}
		if l.in > 0 {
			c.Reset = resetLine(now.Add(l.in+30*time.Second), fiveHour, now)
		}
		return c
	}
	none := limit{dash: true}
	five := func(pct float64, in time.Duration) limit { return limit{pct: pct, in: in, limit: 85} }
	week := func(pct float64, in time.Duration) limit { return limit{pct: pct, in: in, limit: 97} }
	fable := func(pct float64, in time.Duration) limit { return limit{pct: pct, in: in, limit: 95} }
	day := 24 * time.Hour
	rows := []struct {
		id, name, plan string
		active         bool
		limits         [3]limit
	}{
		{"claude:1", "#1 xxxxxxxxxxxxxxx", "Ultra", false, [3]limit{none, week(54, 2*day+22*time.Hour), fable(50, 2*day+22*time.Hour)}},
		{"claude:2", "#2 xxxxxxxxxxxxxxxxxxxx", "Pro", false, [3]limit{five(0, 0), week(100, 12*time.Hour+22*time.Minute), none}},
		{"claude:3", "#3 xxxxxxxxxxxxxxxx", "Ultra", false, [3]limit{five(0, 0), week(100, 2*day+19*time.Hour), fable(100, 2*day+19*time.Hour)}},
		{"claude:4", "#4 xxxxxxxxxxxxxxxxxxxxxxx", "", false, [3]limit{five(0, 0), week(100, 10*time.Hour+22*time.Minute), fable(90, 10*time.Hour+22*time.Minute)}},
		{"claude:5", "#5 xxxxxxxxxxxxxxxxxxxxx", "Ultra", true, [3]limit{five(3, 3*time.Hour+52*time.Minute), week(1, 6*day+20*time.Hour), fable(2, 6*day+20*time.Hour)}},
		{"codex:1", "Codex #1 xxxxxxxxxxxxxxx", "promax", true, [3]limit{none, week(100, 7*time.Hour+28*time.Minute), none}},
	}
	p := tray.Panel{Title: "tycswap",
		Columns: []tray.Column{{ID: "account", Label: "Account"}, {ID: "fiveHour", Label: "5-hour"}, {ID: "sevenDay", Label: "Weekly"}, {ID: "model:Fable", Label: "Fable"}},
		Actions: []tray.Item{{ID: "quit", Title: "Quit"}, {ID: "auto", Title: "Auto-switch", Kind: tray.KindToggle, Checked: true},
			{ID: "autostart", Title: "Start at login", Kind: tray.KindToggle, Checked: true}, {ID: "open", Title: "Open dashboard"}}}
	for _, r := range rows {
		row := tray.PanelRow{ID: r.id, Name: r.name, Plan: r.plan, Active: r.active}
		for i, l := range r.limits {
			row.Cells = append(row.Cells, cell(l, i == 0))
		}
		p.Rows = append(p.Rows, row)
	}
	return p
}

// resetLine mirrors the app's: clock time and time left.
func resetLine(at time.Time, fiveHour bool, now time.Time) string {
	clock := at.Format("15:04")
	if !fiveHour {
		day := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
		switch days := int(day(at).Sub(day(now)).Hours() / 24); {
		case days == 0:
			clock = "Today " + clock
		case days <= 6:
			clock = at.Format("Mon 15:04")
		default:
			clock = at.Format("Mon 2 Jan")
		}
	}
	d := at.Sub(now)
	left := fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	switch {
	case d < time.Hour:
		left = fmt.Sprintf("%dm", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		left = fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return clock + " · " + left
}

func interactive(model tray.Panel) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 2; y < 14; y++ {
		for x := 2; x < 14; x++ {
			img.Set(x, y, color.RGBA{90, 162, 255, 255})
		}
	}
	var icon bytes.Buffer
	_ = png.Encode(&icon, img)
	var mu sync.Mutex
	var shell tray.Tray
	options := tray.Options{Tooltip: "Synthetic tray preview"}
	options.OnPanelAction = func(action tray.PanelAction) {
		encoded, _ := json.Marshal(action)
		fmt.Println(string(encoded))
		if action.Key == "quit" {
			shell.Quit()
			return
		}
		mu.Lock()
		defer mu.Unlock()
		model.Note = ""
		if key, ok := strings.CutPrefix(action.Key, "switch:"); ok {
			provider, _, _ := strings.Cut(key, ":")
			for i := range model.Rows {
				if strings.HasPrefix(model.Rows[i].ID, provider+":") {
					model.Rows[i].Active = model.Rows[i].ID == key
				}
				if model.Rows[i].ID == key {
					model.Note = "Switched to " + strings.Fields(model.Rows[i].Name)[0] + "."
				}
			}
		}
		for i := range model.Actions {
			if model.Actions[i].ID == action.Key && model.Actions[i].Kind == tray.KindToggle {
				model.Actions[i].Checked = !model.Actions[i].Checked
			}
		}
		shell.(tray.PanelTray).SetPanel(model)
	}
	var err error
	shell, err = tray.New(tray.Icon{PNG: icon.Bytes()}, options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	shell.(tray.PanelTray).SetPanel(model)
	go func() {
		for attempt := 0; attempt < 50; attempt++ {
			if openOwnTray() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	time.AfterFunc(10*time.Minute, shell.Quit)
	if err := shell.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func openOwnTray() bool {
	user := windows.NewLazySystemDLL("user32.dll")
	found := false
	callback := windows.NewCallback(func(hwnd, lParam uintptr) uintptr {
		var pid uint32
		user.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid != uint32(os.Getpid()) {
			return 1
		}
		name := make([]uint16, 256)
		user.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		if windows.UTF16ToString(name) != "TycswapTray" {
			return 1
		}
		user.NewProc("PostMessageW").Call(hwnd, 0x8001, 0, 0x0202)
		found = true
		return 0
	})
	user.NewProc("EnumWindows").Call(callback, 0)
	return found
}
