// export and import never carry a Fable login (DESIGN A101).
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/transfer"
)

func TestExportAndImportLeaveTheFableLoginsOut(t *testing.T) {
	f := newFableLoginFixture(t)
	if code, _, errb := f.run(t, "add", "--login", "--lineage", "fable"); code != 0 {
		t.Fatalf("Fable login of Account-1: exit %d, stderr %q", code, errb)
	}
	signInAs(t, "b@example.com", "fable-2")
	if code, _, errb := f.run(t, "add", "--login", "--lineage", "fable"); code != 0 {
		t.Fatalf("Fable login of Account-2: exit %d, stderr %q", code, errb)
	}
	fableDir := paths.FableCredentialsDir(paths.GetBackupRoot())
	before := snapshotDir(t, fableDir)
	if len(before) != 2 {
		t.Fatalf("precondition: credentials-fable/ holds %v", before)
	}

	prevErr := transfer.Stderr
	transfer.Stderr = &strings.Builder{}
	t.Cleanup(func() { transfer.Stderr = prevErr })
	envelope := filepath.Join(t.TempDir(), "accounts.tycswap")
	if code, _, errb := f.run(t, "export", envelope); code != 0 {
		t.Fatalf("export: exit %d, stderr %q", code, errb)
	}
	raw, err := os.ReadFile(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []string{"fable-1", "fable-2", "credentials-fable"} {
		if strings.Contains(string(raw), grant) {
			t.Errorf("the export carries %q:\n%s", grant, raw)
		}
	}
	if !strings.Contains(string(raw), "refresh-token-1") {
		t.Errorf("the export lacks Account-1's default login:\n%s", raw)
	}

	if code, _, errb := f.run(t, "import", envelope, "--force"); code != 0 {
		t.Fatalf("import: exit %d, stderr %q", code, errb)
	}
	if after := snapshotDir(t, fableDir); !sameStrings(before, after) {
		t.Errorf("import changed credentials-fable/:\nbefore %v\nafter  %v", before, after)
	}
}

func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out[e.Name()] = string(raw)
	}
	return out
}
