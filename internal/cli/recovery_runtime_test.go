package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestRecoverySnapshotProjectsScopeAndRequiresCompleteClaudeQuota(t *testing.T) {
	now := time.Now()
	stamp := float64(now.Unix())
	row := reporting.AccountSnapshot{Number: "1", RotationEligible: true, Usage: usage.UsageEntry{FetchedAt: &stamp, LastGood: map[string]any{"five_hour": map[string]any{"pct": 5.0}, "seven_day": map[string]any{"pct": 20.0}, "scoped": []any{map[string]any{"name": "Fable 5", "pct": 100.0, "resets_at": now.Add(time.Hour).Format(time.RFC3339)}}}}}
	a := recoveryAccount(row, "opus")
	if !a.QuotaKnown || !usableAccount([]recovery.Account{a}, "1", "opus", now) {
		t.Fatalf("Opus blocked by Fable %#v", a)
	}
	a = recoveryAccount(row, "fable")
	if !a.QuotaKnown || usableAccount([]recovery.Account{a}, "1", "fable", now) {
		t.Fatalf("Fable not blocked %#v", a)
	}
	delete(row.Usage.LastGood, "five_hour")
	if a := recoveryAccount(row, "opus"); a.QuotaKnown {
		t.Fatal("missing Claude shared quota considered usable")
	}
	row.Provider = "codex"
	if a := recoveryAccount(row, "codex"); !a.QuotaKnown {
		t.Fatal("Codex one-window subscription rejected")
	}
}

func TestRecoveryGitStateIncludesUntrackedWithoutExecutingCommands(t *testing.T) {
	cwd := t.TempDir()
	if data, err := exec.Command("git", "init", "--quiet", cwd).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "new file.txt"), []byte("unfinished"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := gitChangedFiles(cwd)
	if len(files) != 1 || files[0] != "new file.txt" {
		t.Fatalf("%#v", files)
	}
}

func TestRecoveryRuntimeCompatibleRotationIsFirstAndDurable(t *testing.T) {
	sw := fixtureSwitcher(t)
	calls := 0
	runtime := newRecoveryRuntime(sw, func(group, account string) error {
		calls++
		if group != "codex" || account != "2" {
			t.Fatalf("rotation %s/%s", group, account)
		}
		return nil
	})
	event := recovery.Event{Provider: "codex", AccountID: "1", AccountIdentity: recovery.IdentityKey("source@example.com", ""), SessionID: "source", IncidentID: "turn", Group: "codex", Model: "gpt-5", CWD: "/tmp/work", Kind: "turn/completed", Error: "usage_limit", Stopped: true, At: time.Now()}
	if _, err := runtime.store.Record(event); err != nil {
		t.Fatal(err)
	}
	stamp := float64(time.Now().Unix())
	reset := time.Now().Add(time.Hour).Format(time.RFC3339)
	snapshot := reporting.AccountsSnapshot{Accounts: []reporting.AccountSnapshot{
		{Number: "1", Email: "source@example.com", Provider: "codex", IsActive: true, RotationEligible: true, Usage: usage.UsageEntry{FetchedAt: &stamp, LastGood: map[string]any{"seven_day": map[string]any{"pct": 100.0, "resets_at": reset}}}},
		{Number: "2", Provider: "codex", RotationEligible: true, Usage: usage.UsageEntry{FetchedAt: &stamp, LastGood: map[string]any{"seven_day": map[string]any{"pct": 10.0}}}},
	}}
	incidents, err := runtime.EvaluateSnapshot(snapshot)
	if err != nil || calls != 1 || len(incidents) != 1 || incidents[0].Decision.Action != "wait" {
		t.Fatalf("%#v calls %d %v", incidents, calls, err)
	}
	state, err := runtime.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, incident := range state.Incidents {
		if incident.Decision.Action != "wait" || incident.Offered {
			t.Fatalf("rotation not durable %#v", incident)
		}
	}
}

func TestRecoveryIncompleteSavedToolsBlockDespiteManualCheckbox(t *testing.T) {
	runtime, request := reviewedStart(t)
	path := filepath.Join(t.TempDir(), "partial.jsonl")
	if err := os.WriteFile(path, []byte(`{"message":{"content":[{"type":"tool_use","id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, _ := runtime.store.Snapshot()
	event := state.Sessions["source"].Event
	event.TranscriptPath = path
	if _, err := runtime.store.Record(event); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PlanSession(request.Packet, request.Destination, request.ReviewedDigest, true, true); err == nil {
		t.Fatal("partial tool state trusted through checkbox")
	}
}

func TestRecoveryBackgroundHistoryRequiresIndependentlyStoppedSource(t *testing.T) {
	runtime, request := reviewedStart(t)
	path := filepath.Join(t.TempDir(), "background.jsonl")
	data := `{"message":{"content":[{"type":"tool_use","id":"a","input":{"run_in_background":true}}]}}
{"message":{"content":[{"type":"tool_result","tool_use_id":"a"}]}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	state, _ := runtime.store.Snapshot()
	event := state.Sessions["source"].Event
	event.TranscriptPath = path
	if _, err := runtime.store.Record(event); err != nil {
		t.Fatal(err)
	}
	profile := groups.ProfileDir(runtime.sw.Store.SharedRoot(), groups.Opus)
	if err := os.MkdirAll(filepath.Join(profile, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(profile, "sessions", fmt.Sprintf("%d.json", os.Getpid()))
	record, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": "source", "cwd": event.CWD, "status": "idle"})
	if err := os.WriteFile(registry, record, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PlanSession(request.Packet, request.Destination, request.ReviewedDigest, true, true); err == nil {
		t.Fatal("live background source allowed through checkbox")
	}
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PlanSession(request.Packet, request.Destination, request.ReviewedDigest, true, true); err != nil {
		t.Fatalf("stopped source and reviewed background state rejected: %v", err)
	}
}

func TestRecoveryPassiveCodexSourceMustBeIndependentlyStopped(t *testing.T) {
	runtime, request := reviewedStart(t)
	event := recovery.Event{Provider: "codex", SessionID: "codex-source", IncidentID: "turn", Group: "codex", Model: "gpt-5", ModelObserved: true, SourcePID: os.Getpid(), CWD: request.Packet.Source.CWD, Kind: "turn/completed", Stopped: true, At: time.Now()}
	if _, err := runtime.store.Record(event); err != nil {
		t.Fatal(err)
	}
	packet, err := recovery.Prepare(recovery.Source{Provider: "codex", SessionID: event.SessionID, IncidentID: event.IncidentID, CWD: event.CWD})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PlanSession(packet, recovery.Destination{Provider: "claude", Group: "opus", Model: "opus"}, packet.Digest, true, true); err == nil || !strings.Contains(err.Error(), "stop the Codex source") {
		t.Fatalf("passive source can resume %v", err)
	}
}

func TestRecoveryRejectsAnotherBusyEditorInSameWorktreeSubdirectory(t *testing.T) {
	runtime, request := reviewedStart(t)
	cwd := request.Packet.Source.CWD
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := groups.ProfileDir(runtime.sw.Store.SharedRoot(), groups.Fable)
	if err := os.MkdirAll(filepath.Join(profile, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": "peer", "cwd": filepath.Join(cwd, "subdir"), "status": "busy"})
	if err := os.WriteFile(filepath.Join(profile, "sessions", fmt.Sprintf("%d.json", os.Getpid())), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PlanSession(request.Packet, request.Destination, request.ReviewedDigest, true, true); err == nil || !strings.Contains(err.Error(), "another session") {
		t.Fatalf("concurrent editor ignored %v", err)
	}
}

func TestRecoveryForeignGroupOwnershipDoesNotSuppressHandover(t *testing.T) {
	now := time.Now()
	stamp := float64(now.Unix())
	reset := now.Add(time.Hour).Format(time.RFC3339)
	row := func(number, email string, used float64) reporting.AccountSnapshot {
		return reporting.AccountSnapshot{Number: number, Email: email, RotationEligible: true, Usage: usage.UsageEntry{FetchedAt: &stamp, LastGood: map[string]any{"five_hour": map[string]any{"pct": used, "resets_at": reset}, "seven_day": map[string]any{"pct": used, "resets_at": reset}, "scoped": []any{map[string]any{"name": "Fable 5", "pct": used, "resets_at": reset}}}}}
	}
	source := recoveryAccount(row("1", "fable@example.com", 100), "fable")
	foreign := recoveryAccount(row("2", "opus@example.com", 10), "fable")
	applyRecoveryOwner(&source, groups.Owner{Scope: "fable"}, nil, "fable")
	applyRecoveryOwner(&foreign, groups.Owner{Scope: "opus"}, nil, "fable")
	event := recovery.Event{Provider: "claude", SessionID: "source", IncidentID: "turn", Group: "fable", Model: "fable", Stopped: true, Error: "rate_limit", AccountID: "1", AccountIdentity: source.Identity}
	input := recovery.Input{Event: event, CurrentAccount: "1", Accounts: []recovery.Account{source, foreign}, DestinationUsable: true, Now: now}
	if decision := recovery.Evaluate(input); decision.Action != "offer" {
		t.Fatalf("known foreign owner suppressed offer %#v accounts %#v", decision, input.Accounts)
	}
	uncertain := recoveryAccount(row("2", "opus@example.com", 10), "fable")
	applyRecoveryOwner(&uncertain, groups.Owner{Scope: "opus", Uncertain: true}, nil, "fable")
	input.Accounts[1] = uncertain
	if decision := recovery.Evaluate(input); decision.Action != "unknown" || !uncertain.Eligible {
		t.Fatalf("uncertain owner was silently excluded %#v account %#v", decision, uncertain)
	}
}
