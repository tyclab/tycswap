package cli

import (
	"os"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/settings"
)

// The scheduled tick returns an idle group's account to the pool; a dry tick
// only reads (DESIGN A74).
func TestGroupTickReleasesAnIdleGroup(t *testing.T) {
	sw := rotatedSwitcher(t)
	active := groups.ActivePath(sw.BackupDir(), groups.Opus)
	old := sw.Clk.Now().Add(-time.Hour)
	if err := os.Chtimes(active, old, old); err != nil {
		t.Fatal(err)
	}
	tickGroupScopes(sw, settings.Default(), nil, true)
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("a dry tick released the group: %v", err)
	}
	var events []autoswitch.Event
	tickGroupScopes(sw, settings.Default(), func(ev autoswitch.Event) { events = append(events, ev) }, false)
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Fatalf("the idle group kept its account: %v", err)
	}
	if len(events) != 1 || events[0].Kind() != "group-release" || events[0].JSON()["group"] != "opus" {
		t.Fatalf("events = %v", events)
	}
	if got := events[0].JSON()["released"].(map[string]any)["number"]; got != 7 {
		t.Fatalf("released %v, want 7", got)
	}
}

func TestGroupTickKeepsARecentGroup(t *testing.T) {
	sw := rotatedSwitcher(t)
	active := groups.ActivePath(sw.BackupDir(), groups.Opus)
	recent := sw.Clk.Now().Add(-time.Minute)
	if err := os.Chtimes(active, recent, recent); err != nil {
		t.Fatal(err)
	}
	tickGroupScopes(sw, settings.Default(), nil, false)
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("a group idle for one minute lost its account: %v", err)
	}
}
