package autoswitch

import (
	"reflect"
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
)

// ApplyThreshold moves the 7d bar and keeps the counted models the engine was
// built with, the set the default-session policy derived (DESIGN A97).
func TestApplyThresholdKeepsTheCountedModels(t *testing.T) {
	f := newFake()
	s := settings.Default()
	s.Model = strp("Fable")
	e := NewEngine(f, s, func(Event) {}, true)
	e.ApplyThreshold(80)
	f.mu.Lock()
	last := f.pollInputs[len(f.pollInputs)-1]
	f.mu.Unlock()
	if want := []string{"Fable"}; last.threshold != 80 || !reflect.DeepEqual(last.models, want) {
		t.Fatalf("poll inputs after ApplyThreshold = %+v, want threshold 80 models %v", last, want)
	}
	if !reflect.DeepEqual(e.models, []string{"Fable"}) {
		t.Fatalf("engine models %v, want [Fable]", e.models)
	}
}
