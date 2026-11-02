package routingstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLeaseRoundTripAndDeadOwnerRecovery(t *testing.T) {
	root := t.TempDir()
	if err := With(root, func(s *State) error {
		s.Sessions[Key("fable", "live")] = Session{Group: "fable", ID: "live", PID: os.Getpid(), Model: "fable"}
		s.Leases[Key("fable", "request")] = Lease{ID: "request", Group: "fable", Session: "live", Account: "2", Model: "fable", PID: os.Getpid()}
		s.History[Key("fable", "old")] = Session{Group: "fable", ID: "old", PID: 1, Model: "opus", Launch: "previous"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.CheckSwitch("fable", "2") != nil {
		t.Fatal("same account refused")
	}
	if s.CheckSwitch("fable", "3") == nil || s.CheckSwitch("fable", "") == nil {
		t.Fatal("request hold bypassed")
	}
	if s.CheckSwitch("opus", "3") != nil {
		t.Fatal("other profile blocked")
	}
	if s.CheckIdle("") == nil {
		t.Fatal("live session ignored")
	}
	s.Sessions[Key("fable", "live")] = Session{Group: "fable", ID: "live", PID: 1, Model: "fable"}
	s.Prune()
	if len(s.Leases) != 0 || len(s.Sessions) != 0 || len(s.History) != 1 {
		t.Fatal("dead process recovery lost history or retained requests")
	}
}

func TestCorruptStateNeverReleasesHolds(t *testing.T) {
	for _, raw := range []string{`{`, `{"version":2,"sessions":{},"leases":{}}`, `{"version":1,"sessions":{},"leases":{"fable/r":{"id":"r","group":"fable","session":"missing","account":"2","pid":42}}}`} {
		root := t.TempDir()
		path := filepath.Join(root, "routing.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		called := false
		if err := With(root, func(*State) error { called = true; return nil }); err == nil || called {
			t.Fatal("corrupt routing state accepted")
		}
		got, _ := os.ReadFile(path)
		if string(got) != raw {
			t.Fatal("corrupt state overwritten")
		}
	}
}

func TestFamilyRefusesAmbiguousModels(t *testing.T) {
	for _, model := range []string{"", "best", "inherit", "default", "claude-fable-", "fable; true", "fable\nopus"} {
		if _, err := Family(model); err == nil {
			t.Fatalf("accepted %q", model)
		}
	}
	for _, model := range []string{"fable", "claude-fable-5-1[1m]", "opus", "claude-opus-5-5", "sonnet", "haiku"} {
		if _, err := Family(model); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryMustResolveToKnownProfileAndModel(t *testing.T) {
	for _, history := range []string{
		`{"fable/s":{"group":"../elsewhere","id":"s","model":"opus","launch":"l"}}`,
		`{"opus/s":{"group":"fable","id":"s","model":"opus","launch":"l"}}`,
		`{"fable/s":{"group":"fable","id":"s","model":"unknown","launch":"l"}}`,
		`{"fable/s":{"group":"fable","id":"s","model":"opus"}}`,
	} {
		root := t.TempDir()
		raw := `{"version":1,"sessions":{},"leases":{},"history":` + history + `}`
		if err := os.WriteFile(filepath.Join(root, "routing.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(root); err == nil {
			t.Fatalf("accepted invalid resume metadata: %s", history)
		}
	}
}
