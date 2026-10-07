package recovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDurableHandoverReservationAndConfirmedDestinationLink(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	event := Event{SessionID: "source", IncidentID: "turn", Provider: "claude", Kind: "StopFailure", Error: "rate_limit", Stopped: true, At: epoch, CWD: "/tmp/work"}
	if _, err := s.Record(event); err != nil {
		t.Fatal(err)
	}
	plan := LaunchPlan{SourceSessionID: "source", CWD: "/tmp/work", PacketDigest: "reviewed", Destination: Destination{Provider: "codex", Usable: true}}
	if err := s.BeginHandover(plan, "turn"); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(root)
	if err := restarted.BeginHandover(plan, "turn"); err == nil {
		t.Fatal("duplicate destination start accepted")
	}
	if err := restarted.CompleteHandover(plan.CWD, plan.PacketDigest, ""); err == nil {
		t.Fatal("missing destination ID accepted")
	}
	if err := restarted.CompleteHandover(plan.CWD, plan.PacketDigest, "native-codex-id"); err != nil {
		t.Fatal(err)
	}
	state, err := restarted.Snapshot()
	if err != nil || state.Handovers[testWorkspace(plan.CWD)].Status != "active" || state.Handovers[testWorkspace(plan.CWD)].DestinationSessionID != "native-codex-id" {
		t.Fatalf("%#v %v", state, err)
	}
	if err := s.CancelHandover(plan.CWD, plan.PacketDigest); err == nil {
		t.Fatal("active destination silently cancelled")
	}
}

func TestChangedSourceInvalidatesReservation(t *testing.T) {
	s := NewStore(t.TempDir())
	event := Event{SessionID: "source", IncidentID: "turn", Provider: "claude", Kind: "StopFailure", Stopped: true, At: epoch, CWD: "/tmp/work"}
	if _, err := s.Record(event); err != nil {
		t.Fatal(err)
	}
	event.Kind = "UserPromptSubmit"
	event.Stopped = false
	if _, err := s.Record(event); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginHandover(LaunchPlan{SourceSessionID: "source", CWD: "/tmp/work", PacketDigest: "review"}, "turn"); err == nil {
		t.Fatal("busy source accepted")
	}
}

func TestInspectToolsFindsPendingCallsAndBackgroundUncertainty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	data := `{"message":{"role":"assistant","content":[{"type":"tool_use","id":"a","input":{"run_in_background":true}},{"type":"tool_use","id":"b"}]}}
{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a"}]}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := InspectTools(path, "claude")
	if err != nil || state.Pending != 1 || !state.Background || !state.Known {
		t.Fatalf("%#v %v", state, err)
	}
	data = `{"type":"response_item","payload":{"type":"function_call","call_id":"a"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"a"}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err = InspectTools(path, "codex")
	if err != nil || state.Pending != 0 || !state.Known {
		t.Fatalf("%#v %v", state, err)
	}
}

func TestDestinationConfirmationRequiresExactTokenAndProvider(t *testing.T) {
	s := NewStore(t.TempDir())
	event := Event{SessionID: "source", IncidentID: "turn", Provider: "claude", Kind: "StopFailure", Stopped: true, At: epoch, CWD: "/tmp/work"}
	if _, err := s.Record(event); err != nil {
		t.Fatal(err)
	}
	plan := LaunchPlan{SourceSessionID: "source", CWD: "/tmp/work", PacketDigest: "digest", Destination: Destination{Provider: "codex"}}
	if err := s.BeginHandover(plan, "turn"); err != nil {
		t.Fatal(err)
	}
	dest := Event{SessionID: "new-native-id", Provider: "codex", CWD: plan.CWD, At: time.Now()}
	if err := s.ConfirmDestination(dest, plan.CWD, "wrong-token"); err == nil {
		t.Fatal("wrong token confirmed")
	}
	wrong := dest
	wrong.Provider = "claude"
	if err := s.ConfirmDestination(wrong, plan.CWD, "digest"); err == nil {
		t.Fatal("wrong provider confirmed")
	}
	if err := s.ConfirmDestination(dest, plan.CWD, "digest"); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedNestedToolRecordsNeverBecomeKnownIdleState(t *testing.T) {
	cases := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call","input":"unreadable"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","input":{}}]}}`,
		`{"type":"response_item","payload":{"type":"function_call"}}`,
	}
	for i, data := range cases {
		path := filepath.Join(t.TempDir(), "tools.jsonl")
		if err := os.WriteFile(path, []byte(data+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		provider := "claude"
		if i == 2 {
			provider = "codex"
		}
		state, err := InspectTools(path, provider)
		if err != nil || state.Known {
			t.Fatalf("case %d %#v %v", i, state, err)
		}
	}
}

func testWorkspace(cwd string) string { key, _ := WorkspaceKey(cwd); return key }
