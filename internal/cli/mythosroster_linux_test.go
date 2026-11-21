package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

func TestDamagedRosterKeepsMythosSelectionAndExplainsUnavailableState(t *testing.T) {
	for _, tc := range []struct{ name, roster string }{
		{"missing file", ""},
		{"missing accounts", `{"sequence":[1,2,3,4]}`},
		{"missing sequence", `{"accounts":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, source, _ := mythosTickFixture(t, 20, 30, true)
			root := sw.BackupDir()
			if err := store.SelectMythosLogin(root, source); err != nil {
				t.Fatal(err)
			}
			selected := filepath.Join(root, "mythos", "selected")
			before, err := os.ReadFile(selected)
			if err != nil {
				t.Fatal(err)
			}
			if tc.roster == "" {
				if err := os.Remove(sw.SequenceFile); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, sw.SequenceFile, tc.roster)
			}
			runCodex(t, "", "auto", "--once", "--json")
			after, err := os.ReadFile(selected)
			if err != nil || string(before) != string(after) {
				t.Errorf("selection bytes changed after a tick with %s: %q, %v", tc.name, after, err)
			} else {
				t.Log("saved Mythos identity bytes preserved across tick")
			}
			got, err := reporting.Status(sw.Store, true)
			if err != nil {
				t.Fatal(err)
			}
			doc := got.(map[string]any)
			m, _ := doc["mythos"].(map[string]any)
			reason, _ := m["reason"].(string)
			if !strings.Contains(reason, "roster") || !strings.Contains(reason, "unavailable") || m["number"] != nil {
				t.Errorf("status failed to identify the unavailable roster: %v", m)
			} else {
				t.Log("status:", reason)
			}
		})
	}
}
