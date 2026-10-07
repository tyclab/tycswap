package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTransferredEditorBlockedUntilDestinationStoppedAndReclaimed(t *testing.T) {
	store := NewStore(t.TempDir())
	source := Event{Provider: "claude", SessionID: "source", IncidentID: "turn", CWD: "/tmp/work", Kind: "StopFailure", Stopped: true, At: epoch}
	if _, err := store.Record(source); err != nil {
		t.Fatal(err)
	}
	plan := LaunchPlan{SourceSessionID: "source", CWD: "/tmp/work", PacketDigest: "review", Destination: Destination{Provider: "codex"}}
	if err := store.BeginHandover(plan, "turn"); err != nil {
		t.Fatal(err)
	}
	if block, err := store.EditBlock("source", plan.CWD); err != nil || block == "" {
		t.Fatalf("prepared source unblocked %q %v", block, err)
	}
	prompt := source
	prompt.Kind = "UserPromptSubmit"
	prompt.Stopped = false
	if _, err := store.Record(prompt); !errors.Is(err, ErrEditOwned) {
		t.Fatalf("atomic prompt guard %v", err)
	}
	if err := store.CompleteHandover(plan.CWD, "review", "destination"); err != nil {
		t.Fatal(err)
	}
	if block, err := store.EditBlock("destination", plan.CWD); err != nil || block != "" {
		t.Fatalf("rightful destination blocked %q %v", block, err)
	}
	if err := store.Reclaim(plan.CWD, "review", false, true); err == nil {
		t.Fatal("running destination reclaimed")
	}
	if err := store.Reclaim(plan.CWD, "review", true, false); err == nil {
		t.Fatal("background tools not reviewed")
	}
	if err := store.Reclaim(plan.CWD, "review", true, true); err != nil {
		t.Fatal(err)
	}
	if block, err := store.EditBlock("source", plan.CWD); err != nil || block != "" {
		t.Fatalf("source trapped after reclaim %q %v", block, err)
	}
	if block, err := store.EditBlock("destination", plan.CWD); err != nil || block == "" {
		t.Fatalf("destination retained reclaimed ownership %q %v", block, err)
	}
}

func TestWorktreeLeaseBlocksSiblingDirectoryEditors(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	one := filepath.Join(cwd, "one")
	two := filepath.Join(cwd, "two")
	if err := os.Mkdir(one, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(two, 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	event := Event{Provider: "claude", SessionID: "source", IncidentID: "turn", CWD: one, Kind: "StopFailure", Stopped: true, At: epoch}
	if _, err := store.Record(event); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginHandover(LaunchPlan{SourceSessionID: "source", CWD: one, PacketDigest: "review", Destination: Destination{Provider: "codex"}}, "turn"); err != nil {
		t.Fatal(err)
	}
	if block, err := store.EditBlock("source", two); err != nil || block == "" {
		t.Fatalf("sibling editor missed worktree lease %q %v", block, err)
	}
}

func TestAtomicReservationRejectsPeerPromptStartedAfterPlanning(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	source := Event{Provider: "claude", SessionID: "source", IncidentID: "turn", CWD: cwd, Kind: "StopFailure", Stopped: true, At: epoch}
	if _, err := store.Record(source); err != nil {
		t.Fatal(err)
	}
	peer := Event{Provider: "claude", SessionID: "peer", CWD: filepath.Join(cwd, "subdir"), Kind: "UserPromptSubmit", At: epoch}
	if _, err := store.Record(peer); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginHandover(LaunchPlan{SourceSessionID: "source", CWD: cwd, PacketDigest: "review", Destination: Destination{Provider: "codex"}}, "turn"); err == nil {
		t.Fatal("peer started before reservation but was ignored")
	}
}
