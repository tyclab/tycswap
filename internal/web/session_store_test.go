package web

import (
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/reporting"
)

func TestIndependentSessionLabelWithAndWithoutFable(t *testing.T) {
	root := t.TempDir()
	if _, err := reporting.RecordDefaultSessionModel(root, "sess-1", "opus", "mythos/account-identity", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		snap := sampleSnapshot()
		if enabled {
			snap.Fable = map[string]any{"state": "on"}
		}
		server, err := New(Deps{Facade: &fakeFacade{backupDir: root, snap: snap}, Sessions: sampleSessions,
			SessionTitle:  func(string, string, string) string { return "" },
			AuthOverrides: func() AuthOverridesView { return AuthOverridesView{} },
		})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range server.Snapshot().Sessions.Claude {
			if row.SessionID == "sess-1" {
				found = true
				if row.Store != "mythos" {
					t.Errorf("Fable enabled=%v: got %q", enabled, row.Store)
				}
			}
		}
		if !found {
			t.Fatal("registered session missing")
		}
	}
}
