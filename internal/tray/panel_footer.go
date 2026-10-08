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

// panelFooterLayout wraps the footer buttons into rows of width pixels and
// returns the height the rows take. measure gives each button's width.
func panelFooterLayout(actions []Item, width, height, gap int, settings bool, measure func(Item) int) ([]panelFooterButton, int) {
	width = max(1, width)
	x, y := 0, 0
	var buttons []panelFooterButton
	for _, action := range actions {
		if action.ID == "open" || action.Kind == KindToggle && !settings {
			continue
		}
		buttonWidth := min(measure(action), width)
		if x > 0 && x+buttonWidth > width {
			x, y = 0, y+height+gap
		}
		buttons = append(buttons, panelFooterButton{action.ID, x, y, buttonWidth})
		x += buttonWidth + gap
	}
	if len(buttons) == 0 {
		return nil, 0
	}
	return buttons, y + height
}
