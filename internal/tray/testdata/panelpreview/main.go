//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/tyclab/tycswap/internal/tray"
	"golang.org/x/sys/windows"
)

func main() {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 2; y < 14; y++ {
		for x := 2; x < 14; x++ {
			img.Set(x, y, color.RGBA{90, 162, 255, 255})
		}
	}
	var icon bytes.Buffer
	_ = png.Encode(&icon, img)
	model := fixture()
	var mu sync.Mutex
	var shell tray.Tray
	options := tray.Options{Tooltip: "Synthetic tray preview"}
	options.OnPanelAction = func(action tray.PanelAction) error {
		encoded, _ := json.Marshal(action)
		fmt.Println(string(encoded))
		if action.Kind == "command" && action.Key == "quit" {
			shell.Quit()
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		switch action.Kind {
		case "setting-set", "setting-reset":
			for i := range model.Settings {
				if model.Settings[i].ID != action.Scope {
					continue
				}
				for j := range model.Settings[i].Settings {
					field := &model.Settings[i].Settings[j]
					if field.Key != action.Key {
						continue
					}
					if field.ReadOnly != "" {
						return fmt.Errorf("%s", field.ReadOnly)
					}
					field.Value, field.IsDefault, field.Source = action.Value, false, "user"
					if action.Kind == "setting-reset" {
						field.Value, field.IsDefault, field.Source = "85", true, "default"
					}
				}
			}
		case "sort":
			model.SortDescending = model.SortColumn == action.Key && !model.SortDescending
			model.SortColumn = action.Key
			index := 0
			for i, column := range model.Columns {
				if column.ID == action.Key {
					index = i
				}
			}
			sort.SliceStable(model.Rows, func(i, j int) bool {
				a, b := model.Rows[i].Cells[index], model.Rows[j].Cells[index]
				if a == b {
					return model.Rows[i].ID < model.Rows[j].ID
				}
				if model.SortDescending {
					return a > b
				}
				return a < b
			})
		}
		shell.(tray.PanelTray).SetPanel(model)
		return nil
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
	go func() {
		for tick := range time.NewTicker(2 * time.Second).C {
			mu.Lock()
			model.Status = "Synthetic fixture · updated " + tick.Format("15:04:05")
			shell.(tray.PanelTray).SetPanel(model)
			mu.Unlock()
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

func fixture() tray.Panel {
	lo, hi := 0.0, 100.0
	p := tray.Panel{Title: "tycswap preview", Version: "Synthetic data", Status: "Synthetic fixture · connected",
		Columns: []tray.Column{{ID: "account", Label: "Account"}, {ID: "fiveHour", Label: "5-hour"}, {ID: "weekly", Label: "Weekly"}, {ID: "fable", Label: "Fable"}, {ID: "owner", Label: "Used by"}},
		Actions: []tray.Item{{ID: "open", Title: "Open dashboard"}, {ID: "update", Title: "Check for updates"},
			{ID: "install-update", Title: "Update tycswap", Kind: tray.KindUpdate}, {ID: "claude-code", Title: "Update Claude", Kind: tray.KindUpdate},
			{ID: "quit", Title: "Quit"}, {ID: "auto", Title: "Auto-switch", Kind: tray.KindToggle}, {ID: "autostart", Title: "Start at login", Kind: tray.KindToggle, Checked: true}},
	}
	quotas := [][]string{{"0%", "65%", "100%"}, {"0%", "46%", "—"}, {"3%", "97%", "100%"}, {"9%", "88%", "79%"}, {"0%", "100%", "89%"}, {"0%", "48%", "55%"}, {"0%", "97%", "100%"}, {"—", "93%", "—"}}
	for i, name := range []string{"Work", "Business", "Personal", "Max", "Spare", "Personal", "Spare", "Codex #1"} {
		number := i + 1
		id := fmt.Sprintf("claude:%d", number)
		title := fmt.Sprintf("#%d %s", number, name)
		owner := "—"
		if number == 4 {
			owner = "Fable · 2 sessions"
		}
		if number == 6 {
			owner = "Opus · 1 session"
		}
		if number == 8 {
			owner = "Codex"
			id = "codex:1"
			title = name
		}
		targets := []tray.Target{{ID: fmt.Sprintf("switch:claude:%d", number), Label: "Existing / default"}, {ID: fmt.Sprintf("group:fable:%d", number), Label: "Fable"}, {ID: fmt.Sprintf("group:opus:%d", number), Label: "Opus / other"}}
		if number == 4 {
			targets[1].Disabled, targets[1].Reason = true, "Already active for Fable"
			targets[0].Disabled, targets[0].Reason = true, "Reserved by Fable's live sessions"
			targets[2].Disabled, targets[2].Reason = true, "Reserved by Fable's live sessions"
		}
		if number == 2 {
			targets[1].Disabled, targets[1].Reason = true, "Fable start not permitted for this account"
		}
		if number == 6 {
			targets[2].Disabled, targets[2].Reason = true, "Already active for Opus / other"
			targets[0].Disabled, targets[0].Reason = true, "Reserved by Opus / other's live sessions"
			targets[1].Disabled, targets[1].Reason = true, "Reserved by Opus / other's live sessions"
		}
		if number == 8 {
			targets = []tray.Target{{ID: "switch:codex:1", Label: "Codex", Disabled: true, Reason: "Already active for Codex"}}
		}
		quota := quotas[i]
		var details []string
		for window, label := range []string{"5-hour", "Weekly", "Fable"} {
			line := label + ": " + quota[window] + " used · resets in 2d"
			if quota[window] == "—" {
				line = label + ": not reported"
			}
			if number == 8 && window == 2 {
				line = "Fable: not applicable to Codex"
			}
			details = append(details, line)
		}
		if number != 8 {
			details = append(details, "No separate Opus limit reported; shared limits are shown above.")
		}
		details = append(details, "Synthetic usage fixture")
		if number != 8 && quota[2] == "100%" {
			targets[1].Disabled, targets[1].Reason = true, "Fable weekly limit exhausted"
		}
		p.Rows = append(p.Rows, tray.PanelRow{ID: id, Title: title, Active: owner != "—", Cells: []string{title, quota[0], quota[1], quota[2], owner}, Details: details, Targets: targets})
	}
	for _, scope := range []tray.SettingScope{{ID: "", Label: "Shared defaults"}, {ID: "fable", Label: "Fable"}, {ID: "opus", Label: "Opus / other"}} {
		scope.Settings = []tray.PanelSetting{
			{Key: "autoswitch.fiveHourThreshold", Label: "5-hour threshold", Kind: "float", Value: "85", Min: &lo, Max: &hi, Description: "Rotate at this percentage used.", Applies: "Next rotation check", Source: "default", IsDefault: true},
			{Key: "autoswitch.sevenDayThreshold", Label: "Weekly threshold", Kind: "float", Value: "97", Min: &lo, Max: &hi, Description: "Weekly switch threshold.", Applies: "Next rotation check", Source: "user"},
			{Key: "autoswitch.strategy", Label: "Switch strategy", Kind: "choice", Value: "soonest-reset", Choices: []string{"best", "soonest-reset"}, Source: "default", IsDefault: true},
			{Key: "autoswitch.codexEnabled", Label: "Codex auto-switch", Kind: "bool", Value: "false", Source: "default", IsDefault: true},
			{Key: "autoswitch.handoverWaitMinutes", Label: "Handover wait", Kind: "int", Value: "30", Description: "Offer a handover after this many minutes.", Source: "default", IsDefault: true},
		}
		if scope.ID != "" {
			scope.Settings = append(scope.Settings, tray.PanelSetting{Key: "autoswitch.model", Label: "Model limits", Kind: "string", Value: strings.ToLower(scope.ID), Source: "session", ReadOnly: "Model limits follow this group's live sessions."})
		}
		p.Settings = append(p.Settings, scope)
	}
	return p
}
