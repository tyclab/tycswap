package switching

import (
	"strings"
	"testing"
)

func TestDisabledClaudeSwitchTargets(t *testing.T) {
	for _, target := range []string{"1", "2"} {
		t.Run(target, func(t *testing.T) {
			s, f := fableSwitchStore(t)
			fableLive := planCreds("at-f1", "rt-f1", "max")
			setFableLive(t, s, fableLive)
			data, err := s.ReadSequence()
			if err != nil {
				t.Fatal(err)
			}
			rec, _ := accountRec(data, target)
			rec["disabled"] = true
			data.Accounts[target] = record(rec)
			writeSeq(t, s, data)
			before := fableDefaultFiles(t, s)
			for _, action := range []struct {
				name string
				run  func() error
			}{
				{"switch", func() error { _, err := SwitchTo(s, target, true, false); return err }},
				{"force", func() error { _, err := SwitchTo(s, target, true, true); return err }},
				{"prepared", func() error { _, err := performSwitch(s, target, false, false, nil); return err }},
				{"fable", func() error { _, err := SwitchFable(f, target, FableSwitchOptions{}); return err }},
			} {
				t.Run(action.name, func(t *testing.T) {
					if err := action.run(); err == nil || !strings.Contains(err.Error(), "disabled") {
						t.Errorf("expected disabled error for slot %s, got %v", target, err)
					}
					sameFiles(t, before, fableDefaultFiles(t, s))
					if got, _, err := f.Creds.ReadActive(); err != nil || got != fableLive {
						t.Errorf("secondary credential changed, read error %v", err)
					}
				})
			}
		})
	}
}
