package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/recovery"
)

func TestRecoveryHookFailsOpenAndPersistsSafeLifecycle(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var out, errs bytes.Buffer
	streams := ioStreams{in: strings.NewReader(`{"hook_event_name":"StopFailure","session_id":"s","error":"rate_limit","permission_mode":"danger","last_assistant_message":"Bearer secret"}`), out: &out, err: &errs}
	if code := recoveryCommand("tycswap", []string{"record", "--group", "opus", "--model", "opus"}, streams); code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	if code := recoveryCommand("tycswap", []string{"list"}, streams); code != 0 {
		t.Fatal(code)
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "permission_mode") {
		t.Fatal("unsafe payload persisted")
	}
	var state recovery.State
	if err := json.Unmarshal(out.Bytes(), &state); err != nil || state.Sessions["s"].Event.Error != "rate_limit" {
		t.Fatalf("%s %v", out.String(), err)
	}
	streams.in = strings.NewReader("{")
	if code := recoveryCommand("tycswap", []string{"record"}, streams); code != 0 {
		t.Fatal("hook blocked source")
	}
}

func TestRecoveryPlanHasNoImplicitLaunch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	packet, err := recovery.Prepare(recovery.Source{Provider: "claude", SessionID: "s", CWD: "/tmp/work", Objective: "Continue tests"})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"packet": packet, "destination": recovery.Destination{Provider: "codex", Usable: true}, "request": recovery.StartRequest{Explicit: true, ReviewedDigest: packet.Digest, SourceIdle: true, StateKnown: true}}
	data, _ := json.Marshal(input)
	var out, errs bytes.Buffer
	code := recoveryCommand("tycswap", []string{"plan"}, ioStreams{in: bytes.NewReader(data), out: &out, err: &errs})
	if code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	var plan recovery.LaunchPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.Executable != "codex" || plan.SourceSessionID != "s" {
		t.Fatalf("%s %v", out.String(), err)
	}
}

func TestRecoveryExplicitSavedTranscriptPrepareWithoutHook(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Complete parser"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := recoveryCommand("tycswap", []string{"prepare", "--provider", "codex", "--session", "source", "--cwd", "/tmp/work", "--transcript", path}, ioStreams{in: strings.NewReader(""), out: &out, err: &errs})
	if code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	var packet recovery.Packet
	if err := json.Unmarshal(out.Bytes(), &packet); err != nil || len(packet.Source.Messages) != 1 || packet.Source.Messages[0].Text != "Complete parser" {
		t.Fatalf("%s %v", out.String(), err)
	}
}
