package autoswitch

import (
	"reflect"
	"sync"
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

// The models a tick decides with belong to the tick goroutine: ApplyThreshold,
// which the TUI calls beside a running loop, never writes them, whatever the
// settings name.
func TestApplyThresholdNeverWritesTheTicksModels(t *testing.T) {
	f := newFake()
	s := settings.Default()
	s.Model = strp("Fable")
	e := NewEngine(f, s, func(Event) {}, true)
	e.models = []string{"Opus"}
	e.ApplyThreshold(80)
	if !reflect.DeepEqual(e.models, []string{"Opus"}) {
		t.Fatalf("engine models %v after ApplyThreshold, want the tick's [Opus]", e.models)
	}
}

// ApplyThreshold from another goroutine while the tick goroutine reads its
// models: no data race (-race), and the bar applied last is the one pinned.
func TestApplyThresholdConcurrentWithTheTicksModels(t *testing.T) {
	f := newFake()
	s := settings.Default()
	s.Model = strp("Fable")
	e := NewEngine(f, s, func(Event) {}, true)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			e.ApplyThreshold(float64(50 + i%50))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = pollThreshold(e.currentSettings(), e.models)
		}
	}()
	wg.Wait()
	e.ApplyThreshold(75)
	f.mu.Lock()
	last := f.pollInputs[len(f.pollInputs)-1]
	f.mu.Unlock()
	if last.threshold != 75 || !reflect.DeepEqual(last.models, []string{"Fable"}) {
		t.Fatalf("final poll inputs %+v, want threshold 75 models [Fable]", last)
	}
}
