package settings

import (
	"sync"
	"testing"
)

// TestConcurrentSetSettingKeepsEveryUpdate: parallel read-modify-writes of
// different keys all land (they serialize on the settings lock).
func TestConcurrentSetSettingKeepsEveryUpdate(t *testing.T) {
	root := t.TempDir()
	type kv struct{ key, val string }
	var sets []kv
	for _, spec := range SettingSpecs {
		v := "1"
		if _, err := ParseSettingValue(spec, v); err != nil {
			continue
		}
		sets = append(sets, kv{spec.Dotted(), v})
	}
	if len(sets) < 3 {
		t.Skipf("only %d keys accept 1", len(sets))
	}
	var wg sync.WaitGroup
	for _, s := range sets {
		wg.Add(1)
		go func(s kv) {
			defer wg.Done()
			if _, err := SetSetting(root, s.key, s.val); err != nil {
				t.Errorf("SetSetting(%s): %v", s.key, err)
			}
		}(s)
	}
	wg.Wait()
	set := map[string]bool{}
	for _, e := range EffectiveSettings(root) {
		if e.IsSet {
			set[e.Spec.Dotted()] = true
		}
	}
	for _, s := range sets {
		if !set[s.key] {
			t.Errorf("%s lost", s.key)
		}
	}
}
