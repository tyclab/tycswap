package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/storemigrate"
)

// realOldStoreHint is the production hint; every other test in this package
// runs with the hint off so a real old store on the test machine cannot leak
// into their stderr assertions.
var realOldStoreHint = oldStoreHint

func init() { oldStoreHint = func() string { return "" } }

// migrateHome gives the test a temp home holding an old store at this
// platform's first old root and returns (old root, new root), both from the
// path helpers.
func migrateHome(t *testing.T) (string, string) {
	t.Helper()
	migrateIsolated(t)
	oldStoreHint = realOldStoreHint
	t.Cleanup(func() { oldStoreHint = func() string { return "" } })
	old := paths.OldBackupRoots()[0]
	if err := os.MkdirAll(filepath.Join(old, "configs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "sequence.json"), []byte(`{"accounts":{},"sequence":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return old, paths.GetBackupRoot()
}

// migrateIsolated gives the test a temp home (cleanHome) and runs its migrate
// commands against a fake Keychain: the copy's Keychain step (macOS) must
// never read the login Keychain.
func migrateIsolated(t *testing.T) {
	t.Helper()
	cleanHome(t)
	kc := keychain.NewFake()
	prev := runMigrate
	runMigrate = func(o storemigrate.Options) (storemigrate.Report, error) {
		o.Keychain = kc
		return prev(o)
	}
	t.Cleanup(func() { runMigrate = prev })
}

func runMig(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run("tycswap", argv, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	return code, out.String(), errb.String()
}

func TestOldStoreHintOnOtherCommands(t *testing.T) {
	old, _ := migrateHome(t)
	want := "a claude-swap store exists at " + old + "; `tycswap migrate` copies it once"

	_, _, stderr := runMig(t, "alias", "--help")
	if !strings.Contains(stderr, want) || strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr = %q, want exactly the hint line", stderr)
	}
	_, _, stderr = runMig(t, "list", "--json")
	if strings.Contains(stderr, "tycswap migrate") {
		t.Errorf("hint printed in --json mode: %q", stderr)
	}
	_, _, stderr = runMig(t, "migrate", "--dry-run")
	if strings.Contains(stderr, "copies it once") {
		t.Errorf("hint printed for migrate itself: %q", stderr)
	}
}

func TestMigrateCommandCopiesThenRefuses(t *testing.T) {
	old, newRoot := migrateHome(t)
	before, _ := os.ReadFile(filepath.Join(old, "sequence.json"))

	code, out, stderr := runMig(t, "migrate", "--dry-run")
	if code != 0 || !strings.Contains(out, "Would copy "+old) {
		t.Fatalf("dry run = %d, %q, %q", code, out, stderr)
	}
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("dry run created %s", newRoot)
	}

	code, out, stderr = runMig(t, "migrate", "--json")
	if code != 0 {
		t.Fatalf("migrate --json = %d, %q, %q", code, out, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out)
	}
	if doc["from"] != old || doc["to"] != newRoot || doc["dryRun"] != false || doc["files"] != float64(1) {
		t.Errorf("json = %v", doc)
	}
	got, err := os.ReadFile(filepath.Join(newRoot, "sequence.json"))
	if err != nil || string(got) != string(before) {
		t.Errorf("copied sequence.json = %q, %v", got, err)
	}
	if after, _ := os.ReadFile(filepath.Join(old, "sequence.json")); string(after) != string(before) {
		t.Error("old store modified")
	}

	// The new store holds data now: the hint is gone, and a rerun of the
	// complete copy verifies it and writes nothing.
	if _, _, stderr := runMig(t, "alias", "--help"); stderr != "" {
		t.Errorf("hint after migrate: %q", stderr)
	}
	code, out, stderr = runMig(t, "migrate")
	if code != 0 || !strings.Contains(out, "Resumed") || !strings.Contains(out, "already copied") {
		t.Errorf("rerun = %d, %q, %q; want a verified no-op", code, out, stderr)
	}
	// A store changed since refuses.
	if err := os.WriteFile(filepath.Join(newRoot, "sequence.json"), []byte(`{"accounts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runMig(t, "migrate")
	if code != 1 || !strings.Contains(stderr, "other content") {
		t.Errorf("conflicting rerun = %d, %q; want a refusal", code, stderr)
	}
}

func TestMigrateCommandNothingToCopy(t *testing.T) {
	migrateIsolated(t)
	code, out, _ := runMig(t, "migrate", "--json")
	if code != 1 || !strings.Contains(out, `"MigrationError"`) {
		t.Errorf("= %d, %q; want a MigrationError envelope", code, out)
	}
}
