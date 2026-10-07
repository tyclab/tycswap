package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestClaudeAliveStopFailureAndSanitizedPersistence(t *testing.T) {
	e, err := ParseClaude([]byte(`{"hook_event_name":"StopFailure","session_id":"alive","error":"rate_limit","transcript_path":"/tmp/session.jsonl","cwd":"/tmp","permission_mode":"bypassPermissions","last_assistant_message":"Bearer secret","error_details":"secret"}`), "opus", "opus", epoch)
	if err != nil || !e.Stopped || e.Error != "rate_limit" {
		t.Fatalf("%#v %v", e, err)
	}
	raw, _ := json.Marshal(e)
	if string(raw) == "" {
		t.Fatal("empty")
	}
	var persisted map[string]any
	_ = json.Unmarshal(raw, &persisted)
	for _, key := range []string{"permission_mode", "last_assistant_message", "error_details"} {
		if _, ok := persisted[key]; ok {
			t.Fatal(key)
		}
	}
}

func TestCodexSupportedSchemaNotRenderedText(t *testing.T) {
	cases := []struct {
		raw       string
		stopped   bool
		errorType string
	}{
		{`{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","status":"failed","error":{"message":"x","codexErrorInfo":"usageLimitExceeded"}}}}`, true, "usage_limit"},
		{`{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","status":"failed","error":{"message":"usage limit reached","codexErrorInfo":{"httpConnectionFailed":{"httpStatusCode":429}}}}}}`, true, "unknown"},
		{`{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","status":"interrupted","error":{"codexErrorInfo":"usageLimitExceeded"}}}}`, false, ""},
		{`{"method":"error","params":{"threadId":"t","turnId":"u","willRetry":false,"error":{"codexErrorInfo":"usageLimitExceeded"}}}`, false, "usage_limit"},
	}
	for _, tt := range cases {
		e, err := ParseCodex([]byte(tt.raw), "gpt-5", epoch)
		if err != nil || e.Stopped != tt.stopped || e.Error != tt.errorType {
			t.Fatalf("%#v %v", e, err)
		}
	}
	if _, err := ParseCodex([]byte(`{"type":"turn.failed","error":{"message":"usage limit reached"}}`), "gpt-5", epoch); err == nil {
		t.Fatal("rendered exec event accepted")
	}
}

func TestPersistenceDedupDismissalAndSeparateIncidents(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	start := Event{SessionID: "s", AccountID: "a", AccountIdentity: "identity-a", Provider: "claude", Kind: "UserPromptSubmit", Model: "opus", Group: "opus", At: epoch}
	if _, err := s.Record(start); err != nil {
		t.Fatal(err)
	}
	stop := start
	stop.Kind = "StopFailure"
	stop.Error = "rate_limit"
	stop.Stopped = true
	event, err := s.Record(stop)
	if err != nil {
		t.Fatal(err)
	}
	in := input(account("a", time.Hour))
	in.Event = event
	d, err := s.Decide(in)
	if err != nil || !d.NewlyOffered || d.Action != "offer" {
		t.Fatalf("%#v %v", d, err)
	}
	restarted := NewStore(root)
	d, err = restarted.Decide(in)
	if err != nil || d.NewlyOffered || d.Action != "offer" {
		t.Fatalf("offer must stay visible: %#v %v", d, err)
	}
	if err := restarted.Dismiss("s", event.IncidentID); err != nil {
		t.Fatal(err)
	}
	d, err = NewStore(root).Decide(in)
	if err != nil || d.Action != "wait" {
		t.Fatalf("%#v %v", d, err)
	}
	if _, err := s.Record(start); err != nil {
		t.Fatal(err)
	}
	second, err := s.Record(stop)
	if err != nil || second.IncidentID == event.IncidentID {
		t.Fatalf("next incident: %#v %v", second, err)
	}
	in.Event = second
	d, err = s.Decide(in)
	if err != nil || !d.NewlyOffered {
		t.Fatalf("%#v %v", d, err)
	}
}

func TestConcurrentStoresAndCorruptStateFailClosed(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := NewStore(root).Record(Event{SessionID: string(rune('a' + i)), Kind: "Stop", At: epoch})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	s := NewStore(root)
	state, err := s.Snapshot()
	if err != nil || len(state.Sessions) != 8 {
		t.Fatalf("%#v %v", state, err)
	}
	if err := os.WriteFile(s.Path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Record(Event{SessionID: "s", Kind: "Stop", At: epoch}); err == nil {
		t.Fatal("corruption overwritten")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(file).Decide(input(account("a", time.Hour))); err == nil {
		t.Fatal("write failure silently offered")
	}
}

func TestCodexErrorRequiresTurnCompletion(t *testing.T) {
	s := NewStore(t.TempDir())
	e := Event{Provider: "codex", SessionID: "t", IncidentID: "turn", Kind: "error", Error: "usage_limit", At: epoch, Model: "gpt-5"}
	if _, err := s.Record(e); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot()
	if state.Sessions["t"].Idle {
		t.Fatal("error prematurely marks idle")
	}
	e.Kind = "turn/completed"
	e.Error = ""
	e.Stopped = true
	e.At = epoch.Add(time.Second)
	got, err := s.Record(e)
	if err != nil || got.Error != "usage_limit" {
		t.Fatalf("%#v %v", got, err)
	}
}

func TestFailureRetainsTurnAccountDespiteLaterAccountRotation(t *testing.T) {
	store := NewStore(t.TempDir())
	start := Event{Provider: "claude", SessionID: "s", Kind: "UserPromptSubmit", AccountID: "a", AccountIdentity: "identity-a", Model: "opus", CWD: "/tmp/work", At: epoch}
	if _, err := store.Record(start); err != nil {
		t.Fatal(err)
	}
	failure := start
	failure.Kind = "StopFailure"
	failure.Error = "rate_limit"
	failure.Stopped = true
	failure.AccountID = "b"
	failure.AccountIdentity = "identity-b"
	event, err := store.Record(failure)
	if err != nil || event.AccountID != "a" || event.AccountIdentity != "identity-a" {
		t.Fatalf("source binding replaced %#v %v", event, err)
	}
}

func TestFallbackModelCannotOverwriteObservedOrUncertainModel(t *testing.T) {
	store := NewStore(t.TempDir())
	event := Event{Provider: "claude", SessionID: "s", Kind: "SessionStart", Model: "sonnet", ModelObserved: true, At: epoch}
	if _, err := store.Record(event); err != nil {
		t.Fatal(err)
	}
	event.Kind = "UserPromptSubmit"
	event.Model = "opus"
	event.ModelObserved = false
	recorded, err := store.Record(event)
	if err != nil || recorded.Model != "sonnet" {
		t.Fatalf("launch fallback overwrote observed model %#v %v", recorded, err)
	}
	if err := store.ModelChange("s", "sonnet", "opus", epoch); err != nil {
		t.Fatal(err)
	}
	recorded, err = store.Record(event)
	if err != nil || recorded.Model != "" {
		t.Fatalf("uncertain switch assumed successful %#v %v", recorded, err)
	}
	event.Model = "opus"
	event.ModelObserved = true
	recorded, err = store.Record(event)
	if err != nil || recorded.Model != "opus" {
		t.Fatalf("actual model observation rejected %#v %v", recorded, err)
	}
}
