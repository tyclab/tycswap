package autoswitch

import (
	"reflect"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/settings"
)

func TestApplyModelsRetargetsAtNextTick(t *testing.T) {
	f := newFake()
	e := NewEngine(f, settings.Default(), func(Event) {}, true)
	if len(e.models) != 0 {
		t.Fatalf("models %v at start", e.models)
	}
	e.ApplyModels("Fable, Opus")
	if len(e.models) != 0 {
		t.Fatal("ApplyModels wrote the tick goroutine's models directly")
	}
	if got := e.currentSettings().Model; got == nil || *got != "Fable, Opus" {
		t.Fatalf("settings.Model = %v", got)
	}
	f.mu.Lock()
	last := f.pollInputs[len(f.pollInputs)-1]
	f.mu.Unlock()
	if want := settings.ParseModelNames(strp("Fable, Opus")); !reflect.DeepEqual(last.models, want) || last.threshold != 90 {
		t.Fatalf("poll inputs %+v, want threshold 90 models %v", last, want)
	}
	e.adoptPendingModels()
	if want := settings.ParseModelNames(strp("Fable, Opus")); !reflect.DeepEqual(e.models, want) || e.modelCheckDone {
		t.Fatalf("models %v (check done %v), want %v and a fresh name check", e.models, e.modelCheckDone, want)
	}
	e.ApplyModels("")
	e.adoptPendingModels()
	if len(e.models) != 0 || !e.modelCheckDone || e.currentSettings().Model != nil {
		t.Fatalf("clearing: models %v model %v", e.models, e.currentSettings().Model)
	}
	e.adoptPendingModels() // nothing queued: no change
	if len(e.models) != 0 {
		t.Fatal("adopt without a queued set changed the models")
	}
}

// ApplyModels from another goroutine while ticks adopt: no data race (-race).
func TestApplyModelsConcurrentWithAdopt(t *testing.T) {
	e := NewEngine(newFake(), settings.Default(), func(Event) {}, true)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			e.ApplyModels("all")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			e.adoptPendingModels()
			_ = len(e.models)
		}
	}()
	wg.Wait()
}
