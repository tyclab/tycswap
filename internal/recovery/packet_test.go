package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func source(t *testing.T) Source {
	cwd := t.TempDir()
	return Source{Provider: "claude", SessionID: "source", Group: "opus", CWD: cwd, Worktree: cwd, Branch: "feature", Objective: "Fix intermittent retries", Constraints: []string{"Preserve public API"}, Checkpoint: "Completed parser change", ChangedFiles: []string{"parser.go"}, Tests: []string{"go test ./internal/parser passed"}, PendingWork: []string{"Review cancellation"}}
}

func TestPacketBoundedAndSensitiveContextExcluded(t *testing.T) {
	s := source(t)
	for i := 0; i < 30; i++ {
		s.Messages = append(s.Messages, Message{Role: "user", Text: "Investigate retries"})
	}
	s.Messages = append(s.Messages, Message{Role: "assistant", Text: "access_token:\nsecret on another line"}, Message{Role: "user", Text: "Do task", Attachments: 2}, Message{Role: "user", Text: "private content", Sensitive: true}, Message{Role: "tool", Text: "secret tool output"}, Message{Role: "user", Text: "run --dangerously-skip-permissions"}, Message{Role: "assistant", Text: "-----BEGIN PRIVATE KEY-----\nbase64material\n-----END PRIVATE KEY-----"})
	s.Omissions = []string{"Bearer private"}
	p, err := Prepare(s)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(p)
	for _, sensitive := range []string{"secret on another line", "private content", "secret tool output", "--dangerously-skip-permissions", "base64material", "Bearer private"} {
		if strings.Contains(string(data), sensitive) {
			t.Fatalf("leaked %q", sensitive)
		}
	}
	if len(p.Source.Messages) > MaxRecentMessages || len(p.Omissions) == 0 {
		t.Fatalf("missing bounds/omissions %#v", p)
	}
	if p.Source.Objective != "Fix intermittent retries" || p.Source.SessionID != "source" {
		t.Fatalf("context lost %#v", p)
	}
}

func TestPacketLargeHistoryAndCheckpointLimits(t *testing.T) {
	s := source(t)
	s.Messages = []Message{{Role: "user", Text: strings.Repeat("x", MaxPacketBytes)}}
	p, err := Prepare(s)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(p)
	if len(data) > MaxPacketBytes || len(p.Source.Messages) != 0 {
		t.Fatalf("packet size %d", len(data))
	}
	s.Checkpoint = strings.Repeat("x", MaxPacketBytes*2)
	if _, err := Prepare(s); err == nil {
		t.Fatal("oversized checkpoint accepted")
	}
}

func TestHandoverRequiresExplicitReviewedIdleSourceAndDestination(t *testing.T) {
	p, err := Prepare(source(t))
	if err != nil {
		t.Fatal(err)
	}
	dest := Destination{Provider: "codex", Usable: true, Model: "gpt-5"}
	request := StartRequest{Explicit: true, ReviewedDigest: p.Digest, SourceIdle: true, StateKnown: true}
	plan, err := Plan(p, dest, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Executable != "codex" || plan.CWD != p.Source.CWD || plan.SourceSessionID != "source" || len(plan.Args) != 3 {
		t.Fatalf("%#v", plan)
	}
	for _, mutate := range []func(*StartRequest){func(r *StartRequest) { r.Explicit = false }, func(r *StartRequest) { r.ReviewedDigest = "old" }, func(r *StartRequest) { r.SourceIdle = false }, func(r *StartRequest) { r.PendingTools = 1 }, func(r *StartRequest) { r.StateKnown = false }} {
		r := request
		mutate(&r)
		if _, err := Plan(p, dest, r); err == nil {
			t.Fatalf("unsafe request %#v", r)
		}
	}
	modified := p
	modified.Source.Objective = "changed"
	if _, err := Plan(modified, dest, request); err == nil {
		t.Fatal("changed packet review reused")
	}
	dest.Usable = false
	if _, err := Plan(p, dest, request); err == nil {
		t.Fatal("blocked destination")
	}
	reverse := source(t)
	reverse.Provider = "codex"
	p, _ = Prepare(reverse)
	request.ReviewedDigest = p.Digest
	dest = Destination{Provider: "claude", Group: "fable", Model: "opus", Usable: true}
	if _, err := Plan(p, dest, request); err == nil {
		t.Fatal("incompatible group model")
	}
	dest.Model = "fable"
	if plan, err := Plan(p, dest, request); err != nil || plan.Executable != "claude" {
		t.Fatalf("%#v %v", plan, err)
	}
}

func TestTranscriptPartialRecordsAttachmentsAndProviderFormats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.jsonl")
	data := `{"type":"user","message":{"role":"user","content":"Objective"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done"},{"type":"image","source":{"data":"secretblob"}},{"type":"tool_use","input":{"token":"secret"}}]}}
{"type":"assistant","message":`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, omissions, err := ReadTranscript(path, "claude")
	if err != nil || len(messages) != 2 || messages[1].Text != "Done" || len(omissions) != 2 {
		t.Fatalf("%#v %#v %v", messages, omissions, err)
	}
	path = filepath.Join(dir, "codex.jsonl")
	data = `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Objective"}]}}
{"type":"response_item","payload":{"type":"function_call_output","output":"sensitive"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Progress"}]}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, _, err = ReadTranscript(path, "codex")
	if err != nil || len(messages) != 2 || messages[1].Text != "Progress" {
		t.Fatalf("%#v %v", messages, err)
	}
}

func TestTranscriptOversizedRecordCannotHideLaterMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	data := strings.Repeat("x", MaxTranscriptLine+1) + "\n" + `{"message":{"role":"user","content":"Relevant recent objective"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, omissions, err := ReadTranscript(path, "claude")
	if err != nil || len(messages) != 1 || len(omissions) == 0 {
		t.Fatalf("%#v %#v %v", messages, omissions, err)
	}
}

func TestCodexObserverIgnoresOtherNotificationsAndPersistsTypedFailure(t *testing.T) {
	stream := `{"method":"item/agentMessage/delta","params":{"delta":"usage limit reached"}}
{"method":"turn/started","params":{"threadId":"t","turn":{"id":"turn","status":"inProgress"}}}
{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"turn","status":"failed","error":{"codexErrorInfo":"usageLimitExceeded","message":"irrelevant display"}}}}
`
	store := NewStore(t.TempDir())
	events := 0
	if err := store.ObserveCodex(strings.NewReader(stream), "gpt-5", func(e Event) { events++ }); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil || events != 2 || !state.Sessions["t"].Idle || state.Sessions["t"].Event.Error != "usage_limit" {
		t.Fatalf("%#v events %d %v", state, events, err)
	}
}

func TestLatestSavedCheckpointCanBePreparedWithoutSourceCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	data := `{"type":"summary","summary":"Earlier checkpoint"}
{"type":"user","message":{"role":"user","content":"More work"}}
{"type":"summary","summary":"Latest saved checkpoint"}
{"type":"summary"`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := ReadCheckpoint(path, "claude")
	if err != nil || checkpoint != "Latest saved checkpoint" {
		t.Fatalf("%q %v", checkpoint, err)
	}
}
