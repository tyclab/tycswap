package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/web"
)

func TestRecoveryLauncherRemovesSourceCredentialAndProfileOverrides(t *testing.T) {
	plan := recovery.LaunchPlan{CWD: "/tmp/work", PacketDigest: "digest"}
	env := handoverEnvironment([]string{"PATH=/bin", "ANTHROPIC_API_KEY=secret", "OPENAI_API_KEY=secret", "ACCESS_TOKEN=secret", "CODEX_HOME=/source", "CLAUDE_CONFIG_DIR=/source", "TYCSWAP_HANDOVER_DIGEST=stale"}, plan)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "/source") || strings.Contains(joined, "stale") || !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "TYCSWAP_HANDOVER_DIGEST=digest") {
		t.Fatal(joined)
	}
}

func reviewedStart(t *testing.T) (*recoveryRuntime, web.RecoveryStartRequest) {
	t.Helper()
	sw := fixtureSwitcher(t)
	runtime := newRecoveryRuntime(sw, nil)
	cwd := t.TempDir()
	event := recovery.Event{Provider: "claude", SessionID: "source", IncidentID: "turn", Group: "opus", Model: "opus", CWD: cwd, Kind: "StopFailure", Error: "rate_limit", Stopped: true, At: time.Now()}
	if _, err := runtime.store.Record(event); err != nil {
		t.Fatal(err)
	}
	stamp := float64(time.Now().Unix())
	runtime.snapshot = reporting.AccountsSnapshot{Accounts: []reporting.AccountSnapshot{{Number: "1", Provider: "codex", IsActive: true, RotationEligible: true, Usage: usage.UsageEntry{FetchedAt: &stamp, LastGood: map[string]any{"seven_day": map[string]any{"pct": 20.0}}}}}}
	packet, err := recovery.Prepare(recovery.Source{Provider: "claude", SessionID: event.SessionID, IncidentID: event.IncidentID, CWD: cwd, Objective: "Continue fixtures"})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, web.RecoveryStartRequest{Packet: packet, Destination: recovery.Destination{Provider: "codex"}, Explicit: true, PendingConfirmed: true, ReviewedDigest: packet.Digest}
}

func TestRecoveryUnavailableTerminalReturnsActionablePlanWithoutReservation(t *testing.T) {
	runtime, request := reviewedStart(t)
	facade := &recoveryWeb{runtime: runtime, lookPath: func(string) (string, error) { return "", errors.New("not installed") }, command: func(context.Context, string, ...string) *exec.Cmd { t.Fatal("no process may start"); return nil }}
	result, err := facade.Start(request)
	if err != nil || result.Started || result.Plan.Executable != "codex" {
		t.Fatalf("%#v %v", result, err)
	}
	state, err := runtime.store.Snapshot()
	if err != nil || len(state.Handovers) != 0 {
		t.Fatalf("%#v %v", state, err)
	}
}

func TestRecoveryFixtureTerminalRetainsUncertainNativeIDWithoutGuessing(t *testing.T) {
	runtime, request := reviewedStart(t)
	t.Setenv("TYCSWAP_RECOVERY_FIXTURE_PROCESS", "1")
	facade := &recoveryWeb{runtime: runtime, lookPath: func(string) (string, error) { return "fixture", nil }, command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryFixtureProcess$")
	}}
	result, err := facade.Start(request)
	if err != nil || !result.Started {
		t.Fatalf("%#v %v", result, err)
	}
	state, err := runtime.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	handover := state.Handovers[leaseWorkspace(request.Packet.Source.CWD)]
	if handover.Status != "prepared" || handover.DestinationSessionID != "" {
		t.Fatalf("native ID fabricated %#v", handover)
	}
	if _, err := facade.Start(request); err == nil {
		t.Fatal("duplicate unconfirmed launch allowed")
	}
}

func TestRecoveryViewReconcilesDelayedNativeIDWithoutBlockingOrDuplicateWorkers(t *testing.T) {
	runtime, request := reviewedStart(t)
	if err := runtime.store.BeginHandover(recovery.LaunchPlan{SourceSessionID: request.Packet.Source.SessionID, CWD: request.Packet.Source.CWD, PacketDigest: request.Packet.Digest, Destination: request.Destination}, request.Packet.Source.IncidentID); err != nil {
		t.Fatal(err)
	}
	responseFile := filepath.Join(t.TempDir(), "sessions.json")
	data, _ := json.Marshal([]launchedSession{{Tool: "codex", PID: 123, Session: "new-native", CWD: request.Packet.Source.CWD}})
	if err := os.WriteFile(responseFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	facade := &recoveryWeb{runtime: runtime, lookPath: func(string) (string, error) { return "fixture", nil }, token: func(int) (string, string, error) { return request.Packet.Source.CWD, request.Packet.Digest, nil }, command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return exec.CommandContext(ctx, "cat", responseFile)
	}}
	before := time.Now()
	view := facade.View(runtime.snapshot)
	if time.Since(before) > 500*time.Millisecond || view.Handovers[leaseWorkspace(request.Packet.Source.CWD)].Status != "prepared" {
		t.Fatal("GET blocked or pending lease hidden")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not start")
	}
	facade.View(runtime.snapshot)
	if calls.Load() != 1 {
		t.Fatalf("duplicate reconciliation workers %d", calls.Load())
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, _ := runtime.store.Snapshot()
		if state.Handovers[leaseWorkspace(request.Packet.Source.CWD)].Status == "active" {
			if state.Handovers[leaseWorkspace(request.Packet.Source.CWD)].DestinationSessionID != "new-native" {
				t.Fatal("wrong native ID")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("delayed native ID was not confirmed")
}

func TestRecoveryExactTokenNeverConfirmsOriginalSourceOrWrongPair(t *testing.T) {
	runtime, request := reviewedStart(t)
	plan := recovery.LaunchPlan{SourceSessionID: request.Packet.Source.SessionID, CWD: request.Packet.Source.CWD, PacketDigest: request.Packet.Digest, Destination: request.Destination}
	if err := runtime.store.BeginHandover(plan, request.Packet.Source.IncidentID); err != nil {
		t.Fatal(err)
	}
	responseFile := filepath.Join(t.TempDir(), "sessions.json")
	data, _ := json.Marshal([]launchedSession{{Tool: "codex", PID: 123, Session: plan.SourceSessionID, CWD: plan.CWD}, {Tool: "codex", PID: 124, Session: "wrong-token-native", CWD: plan.CWD}})
	if err := os.WriteFile(responseFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	facade := &recoveryWeb{runtime: runtime, lookPath: func(string) (string, error) { return "fixture", nil }, token: func(pid int) (string, string, error) {
		if pid == 123 {
			return plan.CWD, plan.PacketDigest, nil
		}
		return plan.CWD, "wrong", nil
	}, command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "cat", responseFile)
	}}
	if facade.confirmDestination("fixture", plan) {
		t.Fatal("source or wrong-token process became destination")
	}
	state, _ := runtime.store.Snapshot()
	if state.Handovers[leaseWorkspace(plan.CWD)].Status != "prepared" || state.Handovers[leaseWorkspace(plan.CWD)].DestinationSessionID != "" {
		t.Fatal("unknown launch changed ownership")
	}
}

func TestRecoveryFixtureProcess(t *testing.T) {
	if os.Getenv("TYCSWAP_RECOVERY_FIXTURE_PROCESS") != "1" {
		return
	}
	os.Stdout.WriteString("[]\n")
	os.Exit(0)
}
