package web

import "testing"

// Settings have one scope since session groups retired (DESIGN A78).
func TestScopedSettingSaveIsRefused(t *testing.T) {
	h := newHarness(t)
	value := "90"
	if _, err := h.s.SaveSetting("fable", "autoswitch.fiveHourThreshold", &value); err == nil {
		t.Fatal("a group-scoped setting was saved")
	}
}
