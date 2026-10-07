package tray

type panelFooterButton struct {
	id          string
	x, y, width int
}

func panelCommandEnabled(panel Panel, action Item) bool {
	if action.Disabled {
		return false
	}
	if !panel.Offline {
		return true
	}
	switch action.ID {
	case "open", "quit", "update", "install-update", "claude-code", "autostart":
		return true
	}
	return false
}

func panelFooterLayout(actions []Item, width int, settings bool) ([]panelFooterButton, int) {
	width = max(1, width)
	x, y := 0, 0
	var buttons []panelFooterButton
	for _, action := range actions {
		if action.ID == "open" || action.Kind == KindToggle && !settings {
			continue
		}
		buttonWidth := 96
		if action.ID == "update" {
			buttonWidth = 140
		}
		if action.Kind == KindToggle {
			buttonWidth = 160
		}
		if action.Kind == KindUpdate {
			buttonWidth = 136
		}
		buttonWidth = min(buttonWidth, width)
		if x > 0 && x+buttonWidth > width {
			x, y = 0, y+34
		}
		buttons = append(buttons, panelFooterButton{action.ID, x, y, buttonWidth})
		x += buttonWidth + 6
	}
	return buttons, y + 58
}
