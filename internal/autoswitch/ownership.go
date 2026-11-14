package autoswitch

import "strings"

func (e *Engine) reportOwnershipBlock(current string, headroom map[string]*float64) bool {
	sw, ok := e.sw.(interface{ RotationBlockReasons() map[string]string })
	if !ok {
		return false
	}
	blocked := sw.RotationBlockReasons()
	keys := make([]string, 0, len(blocked))
	for num := range blocked {
		keys = append(keys, num)
	}
	var details []string
	for _, num := range sortNumeric(keys) {
		if num == current {
			continue
		}
		if h := headroom[num]; h != nil && *h <= 0 {
			continue
		}
		details = append(details, blocked[num])
	}
	if len(details) == 0 {
		return false
	}
	e.emit(NoSwitchEvent{Ts: e.nowISO(), Reason: "accounts-in-use", Detail: strings.Join(details, " ")})
	return true
}
