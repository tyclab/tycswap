package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/storenames"
)

func writeT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readT(t *testing.T, path string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// TestEmailFileNamesRenamesOnce: raw-email files of a store written before
// the security pass are renamed to the encoded names, once; reads through
// credstore then find them.
func TestEmailFileNamesRenamesOnce(t *testing.T) {
	host := newTestHost(t, platform.Linux, nil, nil, map[string]string{"1": "a@x.com", "2": "b@x.com"}, true)
	cfgDir, credDir := host.ConfigsDir(), host.CredentialsDir()
	writeT(t, filepath.Join(cfgDir, ".claude-config-1-a@x.com.json"), `{"oauthAccount":{}}`)
	writeT(t, filepath.Join(credDir, ".creds-1-a@x.com.enc"), "Y3JlZHMtYQ==") // "creds-a"
	writeT(t, filepath.Join(credDir, ".creds-1-a@x.com.enc.prev"), "cHJldg==")
	writeT(t, filepath.Join(credDir, ".creds-None-b@x.com.enc"), "bm9uZQ==")

	notices := Run(host)
	if len(notices) != 1 || !strings.Contains(notices[0], "renamed 4 backup file(s)") {
		t.Fatalf("notices = %v", notices)
	}
	for _, p := range []string{
		filepath.Join(cfgDir, storenames.ConfigFile("1", "a@x.com")),
		filepath.Join(credDir, storenames.CredsFile("1", "a@x.com")),
		filepath.Join(credDir, storenames.CredsPrevFile("1", "a@x.com")),
		filepath.Join(credDir, storenames.CredsFile("None", "b@x.com")),
	} {
		if _, ok := readT(t, p); !ok {
			t.Errorf("%s missing after the rename", filepath.Base(p))
		}
	}
	entries, _ := os.ReadDir(credDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "@") {
			t.Errorf("raw-email name left behind: %s", e.Name())
		}
	}
	if v, _ := host.creds.ReadBackup("1", "a@x.com"); v != "creds-a" {
		t.Errorf("ReadBackup after rename = %q", v)
	}
	applied := loadApplied(host.StateFilePath())
	if _, ok := applied["email_file_names"]; !ok {
		t.Fatalf("not recorded: %v", applied)
	}
	// A second run does nothing.
	if again := Run(host); len(again) != 0 {
		t.Fatalf("second run notices = %v", again)
	}
}

// TestEmailFileNamesConflictsAndUnsafeEmails: an identical leftover is removed,
// a differing one is left with the encoded file untouched, and a slot whose
// roster email could name a path outside the store is never touched.
func TestEmailFileNamesConflictsAndUnsafeEmails(t *testing.T) {
	outside := t.TempDir()
	evil := "a/../../../" + filepath.Base(outside) + "/x"
	host := newTestHost(t, platform.Linux, nil, nil, map[string]string{"1": "a@x.com", "2": "b@x.com", "3": evil}, true)
	cfgDir := host.ConfigsDir()
	writeT(t, filepath.Join(cfgDir, ".claude-config-1-a@x.com.json"), "same")
	writeT(t, filepath.Join(cfgDir, storenames.ConfigFile("1", "a@x.com")), "same")
	writeT(t, filepath.Join(cfgDir, ".claude-config-2-b@x.com.json"), "old")
	writeT(t, filepath.Join(cfgDir, storenames.ConfigFile("2", "b@x.com")), "new")

	Run(host)
	if _, ok := readT(t, filepath.Join(cfgDir, ".claude-config-1-a@x.com.json")); ok {
		t.Error("identical leftover not removed")
	}
	if v, _ := readT(t, filepath.Join(cfgDir, storenames.ConfigFile("2", "b@x.com"))); v != "new" {
		t.Errorf("encoded file changed to %q", v)
	}
	if v, _ := readT(t, filepath.Join(cfgDir, ".claude-config-2-b@x.com.json")); v != "old" {
		t.Errorf("conflicting leftover changed to %q", v)
	}
	if _, ok := loadApplied(host.StateFilePath())["email_file_names"]; !ok {
		t.Error("a conflict that a retry cannot resolve kept the migration pending")
	}
	if storenames.ValidEmail(evil) {
		t.Fatal("the traversal email passed validation")
	}
	// A legacy name built from it would resolve outside the store; plant a
	// file there and check it is neither moved nor removed.
	planted := filepath.Join(cfgDir, storenames.LegacyConfigFile("3", evil))
	writeT(t, planted, "outside")
	if !strings.HasPrefix(planted, outside) {
		t.Fatalf("test setup: %s is not under %s", planted, outside)
	}
	Run(host) // already applied: nothing runs, and nothing would touch it
	if _, _, err := migrateEmailFileNames(host); err != nil {
		t.Fatal(err)
	}
	if v, ok := readT(t, planted); !ok || v != "outside" {
		t.Errorf("file outside the store was touched: %q %v", v, ok)
	}
}
