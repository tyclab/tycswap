package web

import (
	"reflect"
	"testing"
)

func TestLocalSharedModelSaveAndResetRetargetHostedEngine(t *testing.T) {
	h := newHarness(t)
	model := "Fable"
	if _, err := h.s.SaveSetting("", modelSettingKey, &model); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.SaveSetting("", modelSettingKey, nil); err != nil {
		t.Fatal(err)
	}
	if calls := h.auto.Calls(); !reflect.DeepEqual(calls, []string{`ApplyModels("Fable")`, `ApplyModels("")`}) {
		t.Fatalf("local model path %v", calls)
	}
}

// Settings have one scope since session groups retired (DESIGN A78).
func TestScopedSettingSaveIsRefused(t *testing.T) {
	h := newHarness(t)
	value := "90"
	if _, err := h.s.SaveSetting("fable", "autoswitch.fiveHourThreshold", &value); err == nil {
		t.Fatal("a group-scoped setting was saved")
	}
}
