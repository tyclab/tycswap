package storemigrate

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

// snapshot records every path under root with its mode, mtime and contents, so
// a test can prove the old store came through a run byte-for-byte untouched.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		v := fi.Mode().String() + "|" + fi.ModTime().Format(time.RFC3339Nano)
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			v += "|->" + l
		case fi.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			v += "|" + string(b)
		}
		out[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, p, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// oldStore lays down a small store in the old layout and returns its root.
func oldStore(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "claude-swap")
	write(t, filepath.Join(root, "sequence.json"), `{"accounts":{"1":{"email":"a@example.com"}},"sequence":[1]}`, 0o644)
	write(t, filepath.Join(root, "settings.json"), `{}`, 0o644)
	write(t, filepath.Join(root, "mappings.json"), `{}`, 0o644)
	write(t, filepath.Join(root, "credentials", ".creds-1-a@example.com.enc"), "c2VjcmV0", 0o644)
	write(t, filepath.Join(root, "configs", ".claude-config-1-a@example.com.json"), `{}`, 0o644)
	write(t, filepath.Join(root, "cache", "usage.json"), `{}`, 0o644)
	write(t, filepath.Join(root, "claude-swap.log"), "log\n", 0o644)
	write(t, filepath.Join(root, "claude-swap.log.1"), "older\n", 0o644)
	write(t, filepath.Join(root, ".lock"), "", 0o644)
	write(t, filepath.Join(root, "sessions", "1-a_example.com", ".credentials.json"), "{}", 0o644)
	write(t, filepath.Join(root, "sessions", "1-a_example.com", ".cswap-shared.json"), `{"items":[]}`, 0o644)
	write(t, filepath.Join(root, "codex", "sequence.json"), `{"accounts":[]}`, 0o644)
	if runtime.GOOS != "windows" {
		if err := os.Symlink("/nonexistent/.claude/settings.json", filepath.Join(root, "sessions", "1-a_example.com", "settings.json")); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRunCopiesOnceAndLeavesOldStoreUntouched(t *testing.T) {
	old := oldStore(t)
	before := snapshot(t, old)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	linux := platform.Linux

	rep, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.From != old || rep.To != newRoot || rep.DryRun {
		t.Errorf("report header = %+v", rep)
	}

	for rel, want := range map[string]string{
		"sequence.json":                                 `{"accounts":{"1":{"email":"a@example.com"}},"sequence":[1]}`,
		"credentials/.creds-1-a@example.com.enc":        "c2VjcmV0",
		"configs/.claude-config-1-a@example.com.json":   `{}`,
		"tycswap.log":                                   "log\n",
		"tycswap.log.1":                                 "older\n",
		"sessions/1-a_example.com/.tycswap-shared.json": `{"items":[]}`,
		"codex/sequence.json":                           `{"accounts":[]}`,
	} {
		p := filepath.Join(newRoot, filepath.FromSlash(rel))
		b, err := os.ReadFile(p)
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", rel, b, err, want)
		}
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %v, want 0600", rel, fi.Mode().Perm())
			}
		}
	}
	for _, gone := range []string{"claude-swap.log", "sessions/1-a_example.com/.cswap-shared.json"} {
		if _, err := os.Lstat(filepath.Join(newRoot, filepath.FromSlash(gone))); !os.IsNotExist(err) {
			t.Errorf("%s copied under its old name", gone)
		}
	}
	if runtime.GOOS != "windows" {
		for _, d := range []string{"", "credentials", "sessions", "sessions/1-a_example.com", "codex"} {
			fi, err := os.Stat(filepath.Join(newRoot, filepath.FromSlash(d)))
			if err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("dir %q mode = %v, %v; want 0700", d, fi.Mode().Perm(), err)
			}
		}
		link, err := os.Readlink(filepath.Join(newRoot, "sessions", "1-a_example.com", "settings.json"))
		if err != nil || link != "/nonexistent/.claude/settings.json" {
			t.Errorf("symlink = %q, %v; want the link itself copied", link, err)
		}
	}

	if after := snapshot(t, old); !equalMaps(before, after) {
		t.Errorf("old store changed:\nbefore %v\nafter  %v", before, after)
	}

	// A second run of a complete copy writes nothing and verifies everything.
	rep2, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux})
	if err != nil || !rep2.Resumed || len(rep2.Entries) != 0 || len(rep2.Verified) == 0 {
		t.Errorf("second Run = %+v, %v; want a resumed no-op", rep2, err)
	}
}

func TestRunRefusesNonEmptyStore(t *testing.T) {
	old := oldStore(t)
	before := snapshot(t, old)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	write(t, filepath.Join(newRoot, "sequence.json"), `{"accounts":{}}`, 0o600)
	linux := platform.Linux

	_, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), newRoot) || !strings.Contains(err.Error(), "sequence.json") {
		t.Fatalf("err = %v, want ErrConflict naming %s and the file", err, newRoot)
	}
	b, _ := os.ReadFile(filepath.Join(newRoot, "sequence.json"))
	if string(b) != `{"accounts":{}}` {
		t.Errorf("refused run modified the new store: %q", b)
	}
	if _, err := os.Stat(filepath.Join(newRoot, "settings.json")); !os.IsNotExist(err) {
		t.Errorf("refused run copied files")
	}
	if !equalMaps(before, snapshot(t, old)) {
		t.Errorf("refused run changed the old store")
	}
}

func TestRunCopiesOverThrowawayOnly(t *testing.T) {
	old := oldStore(t)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	write(t, filepath.Join(newRoot, "tycswap.log"), "fresh\n", 0o600)
	write(t, filepath.Join(newRoot, "cache", "update_check.json"), "{}", 0o600)
	if !IsEmpty(newRoot) {
		t.Fatal("a store with only a log and a cache counts as empty")
	}
	linux := platform.Linux
	if _, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	old := oldStore(t)
	before := snapshot(t, old)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	linux := platform.Linux

	rep, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, DryRun: true, Platform: &linux})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !rep.DryRun {
		t.Error("report not marked dry run")
	}
	_, files, _ := rep.Counts()
	if files == 0 {
		t.Error("dry run listed no files")
	}
	found := false
	for _, e := range rep.Entries {
		if e.From == "claude-swap.log" && e.To == "tycswap.log" {
			found = true
		}
	}
	if !found {
		t.Errorf("dry run does not show the log rename: %+v", rep.Entries)
	}
	if _, err := os.Lstat(newRoot); !os.IsNotExist(err) {
		t.Errorf("dry run created the new store: %v", err)
	}
	if !equalMaps(before, snapshot(t, old)) {
		t.Error("dry run changed the old store")
	}
}

func TestNoOldStore(t *testing.T) {
	linux := platform.Linux
	_, err := Run(Options{NewRoot: filepath.Join(t.TempDir(), "n"), OldRoots: []string{filepath.Join(t.TempDir(), "absent")}, Platform: &linux})
	if !errors.Is(err, ErrNoOldStore) {
		t.Errorf("err = %v, want ErrNoOldStore", err)
	}
}

func TestFindOldPrefersFirstWithData(t *testing.T) {
	empty := t.TempDir()
	old := oldStore(t)
	if got, ok := FindOld([]string{filepath.Join(t.TempDir(), "absent"), empty, old}); !ok || got != old {
		t.Errorf("FindOld = %q, %v; want %q", got, ok, old)
	}
}

func TestHint(t *testing.T) {
	old := oldStore(t)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	want := "a cswap store exists at " + old + "; `tycswap migrate` copies it once"
	if got := Hint(newRoot, []string{old}); got != want {
		t.Errorf("Hint = %q, want %q", got, want)
	}
	write(t, filepath.Join(newRoot, "sequence.json"), "{}", 0o600)
	if got := Hint(newRoot, []string{old}); got != "" {
		t.Errorf("Hint with a populated store = %q, want none", got)
	}
	if got := Hint(filepath.Join(t.TempDir(), "x"), []string{filepath.Join(t.TempDir(), "absent")}); got != "" {
		t.Errorf("Hint without an old store = %q, want none", got)
	}
}

func TestKeychainItemsCopiedNeverDeleted(t *testing.T) {
	old := oldStore(t)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	kc := keychain.NewFake()
	mustSet := func(svc, acct, v string) {
		if err := kc.Set(svc, acct, v); err != nil {
			t.Fatal(err)
		}
	}
	mustSet(keychain.OldBackupService, "account-1-a@example.com", "blob")
	mustSet(keychain.OldBackupService, "account-1-a@example.com.prev", "older")
	oldSess := sessprofile.KeychainServiceName(filepath.Join(old, "sessions", "1-a_example.com"))
	mustSet(oldSess, keychain.AccountName(), "sess")
	mac := platform.MacOS

	dry, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, DryRun: true, Platform: &mac, Keychain: kc})
	if err != nil || len(dry.Keychain) != 3 {
		t.Fatalf("dry run keychain = %+v, %v; want 3 items", dry.Keychain, err)
	}
	if kc.Exists(keychain.BackupService, "account-1-a@example.com") {
		t.Fatal("dry run wrote a Keychain item")
	}

	rep, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &mac, Keychain: kc})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Keychain) != 3 {
		t.Errorf("copied %d Keychain items, want 3: %+v", len(rep.Keychain), rep.Keychain)
	}
	newSess := sessprofile.KeychainServiceName(filepath.Join(newRoot, "sessions", "1-a_example.com"))
	for _, c := range []struct{ svc, acct, want string }{
		{keychain.BackupService, "account-1-a@example.com", "blob"},
		{keychain.BackupService, "account-1-a@example.com.prev", "older"},
		{newSess, keychain.AccountName(), "sess"},
		{keychain.OldBackupService, "account-1-a@example.com", "blob"},
		{oldSess, keychain.AccountName(), "sess"},
	} {
		if v, ok, _ := kc.Get(c.svc, c.acct); !ok || v != c.want {
			t.Errorf("%s/%s = %q, %v; want %q", c.svc, c.acct, v, ok, c.want)
		}
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestRunResumesAnInterruptedCopy: a rerun after a copy that stopped part-way
// copies what is missing, verifies what is there, removes a leftover temp
// file, and keeps the new store's own log.
func TestRunResumesAnInterruptedCopy(t *testing.T) {
	old := oldStore(t)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	linux := platform.Linux
	if _, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux}); err != nil {
		t.Fatal(err)
	}
	// Simulate the interruption: later files never arrived, a temp file was
	// left, and a command since wrote its own log line.
	for _, rel := range []string{"settings.json", "mappings.json", "codex/sequence.json"} {
		if err := os.Remove(filepath.Join(newRoot, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(newRoot, "sessions")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(newRoot, "configs", ".tycswap-migrate-123.tmp"), "half", 0o600)
	write(t, filepath.Join(newRoot, "tycswap.log"), "new log\n", 0o600)

	rep, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !rep.Resumed {
		t.Error("not reported as resumed")
	}
	for _, rel := range []string{"settings.json", "mappings.json", "codex/sequence.json", "sessions/1-a_example.com/.tycswap-shared.json"} {
		if _, err := os.Stat(filepath.Join(newRoot, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s not copied on resume: %v", rel, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(newRoot, "tycswap.log")); string(b) != "new log\n" {
		t.Errorf("the new store's log was overwritten: %q", b)
	}
	if _, err := os.Stat(filepath.Join(newRoot, "configs", ".tycswap-migrate-123.tmp")); !os.IsNotExist(err) {
		t.Error("leftover temp file kept")
	}
	verified := strings.Join(rep.Verified, ",")
	if !strings.Contains(verified, "sequence.json") {
		t.Errorf("verified = %v", rep.Verified)
	}
}

// TestRunResumeRefusesConflictsAndForeignData: a file changed since the
// interrupted copy, or data that is not from the old store, refuses the rerun
// before anything is written.
func TestRunResumeRefusesConflictsAndForeignData(t *testing.T) {
	linux := platform.Linux
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, newRoot string)
		want  error
	}{
		{"changed file", func(t *testing.T, newRoot string) {
			write(t, filepath.Join(newRoot, "settings.json"), `{"changed":true}`, 0o600)
		}, ErrConflict},
		{"foreign file", func(t *testing.T, newRoot string) {
			write(t, filepath.Join(newRoot, "configs", "mine.json"), `{}`, 0o600)
		}, ErrNotEmpty},
		{"file where a dir belongs", func(t *testing.T, newRoot string) {
			if err := os.RemoveAll(filepath.Join(newRoot, "sessions")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(newRoot, "sessions"), "x", 0o600)
		}, ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := oldStore(t)
			newRoot := filepath.Join(t.TempDir(), "tycswap")
			if _, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux}); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(newRoot, "mappings.json")); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, newRoot)
			_, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux})
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), newRoot) {
				t.Fatalf("err = %v, want %v naming the store", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(newRoot, "mappings.json")); !os.IsNotExist(err) {
				t.Error("a refused resume copied files")
			}
		})
	}
}

// TestKeychainItemsTooLargeGoToTheFile: an old Keychain item over `security
// -i`'s stdin line (the old tool stored such items through argv) is written
// to the file tycswap reads for it instead of failing the copy: the Claude
// backup and its .prev as base64 .enc files, the Codex snapshot as its JSON
// file, the session profile's credential as .credentials.json, each 0600.
// The report names the file, items that fit still go to the Keychain, and a
// rerun verifies the files instead of refusing them.
func TestKeychainItemsTooLargeGoToTheFile(t *testing.T) {
	old := oldStore(t)
	// On macOS the credential lives in the Keychain, not beside it.
	if err := os.Remove(filepath.Join(old, "credentials", ".creds-1-a@example.com.enc")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(old, "codex", "sequence.json"), `{"accounts":{"1":{"account_key":"k1"}},"activeAccountKey":null}`, 0o644)
	// Slot 2 has an .enc file, which credstore serves first: its Keychain
	// item is stale, whatever its size.
	write(t, filepath.Join(old, "sequence.json"), `{"accounts":{"1":{"email":"a@example.com"},"2":{"email":"b@example.com"}},"sequence":[1,2]}`, 0o644)
	write(t, filepath.Join(old, "credentials", ".creds-2-b@example.com.enc"), "c2VjcmV0", 0o644)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	kc := keychain.NewFake()
	big := strings.Repeat("x", keychain.SecurityStdinLineLimit)
	kc.Seed(keychain.OldBackupService, "account-1-a@example.com", big)
	kc.Seed(keychain.OldBackupService, "account-2-b@example.com", big+"stale")
	kc.Seed(keychain.OldBackupService, "account-1-a@example.com.prev", big+"p")
	kc.Seed(keychain.OldBackupService, "account-None-a@example.com", "small")
	kc.Seed(keychain.OldCodexService, authfile.FileKey("k1"), big+"c")
	oldSess := sessprofile.KeychainServiceName(filepath.Join(old, "sessions", "1-a_example.com"))
	kc.Seed(oldSess, keychain.AccountName(), big+"s")
	mac := platform.MacOS

	rep, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &mac, Keychain: kc})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	b64 := func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
	wantFiles := map[string]string{
		"credentials/.creds-1-a@example.com.enc":                b64(big),
		"credentials/.creds-1-a@example.com.enc.prev":           b64(big + "p"),
		"codex/credentials/" + authfile.FileKey("k1") + ".json": big + "c",
		"sessions/1-a_example.com/.credentials.json":            big + "s",
	}
	for rel, want := range wantFiles {
		p := filepath.Join(newRoot, filepath.FromSlash(rel))
		b, err := os.ReadFile(p)
		if err != nil || string(b) != want {
			t.Errorf("%s: %v, content matches = %v", rel, err, string(b) == want)
		}
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %v, want 0600", rel, fi.Mode().Perm())
			}
		}
	}
	for _, a := range []string{"account-1-a@example.com", "account-1-a@example.com.prev"} {
		if kc.Exists(keychain.BackupService, a) {
			t.Errorf("%s was stored in the Keychain although it does not fit", a)
		}
	}
	if v, ok, _ := kc.Get(keychain.BackupService, "account-None-a@example.com"); !ok || v != "small" {
		t.Error("the item that fits was not copied to the Keychain")
	}
	files := map[string]bool{}
	for _, it := range rep.Keychain {
		if it.File != "" {
			files[it.File] = true
		}
	}
	for rel := range wantFiles {
		if !files[rel] {
			t.Errorf("report does not name %s; keychain = %+v", rel, rep.Keychain)
		}
	}
	if len(rep.Keychain) != 5 {
		t.Errorf("reported %d Keychain items, want 5 (the stale slot-2 item is not one)", len(rep.Keychain))
	}
	if b, _ := os.ReadFile(filepath.Join(newRoot, "credentials", ".creds-2-b@example.com.enc")); string(b) != "c2VjcmV0" {
		t.Errorf("slot 2's .enc = %q, want the old store's file, which it serves first", b)
	}
	if kc.Exists(keychain.BackupService, "account-2-b@example.com") {
		t.Error("the stale slot-2 item was copied")
	}
	if !containsPath(rep.Skipped, "sessions/1-a_example.com/.credentials.json") {
		t.Errorf("the profile's seed file was not reported skipped: %v", rep.Skipped)
	}
	// The session profile reads the file now; credstore and the Codex store
	// read their files first by their own rules.
	if creds, ok := sessprofile.ReadSessionCredentials(kc, filepath.Join(newRoot, "sessions", "1-a_example.com")); !ok || creds != big+"s" {
		t.Errorf("session credential = %q, %v", len(creds), ok)
	}

	rep2, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &mac, Keychain: kc})
	if err != nil || !rep2.Resumed || len(rep2.Entries) != 0 || len(rep2.Keychain) != 0 {
		t.Errorf("rerun = %+v, %v; want a verified no-op", rep2, err)
	}
	// A fallback file with other content is a conflict, as any copied file is.
	write(t, filepath.Join(newRoot, "codex", "credentials", authfile.FileKey("k1")+".json"), "other", 0o600)
	if _, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &mac, Keychain: kc}); !errors.Is(err, ErrConflict) {
		t.Errorf("rerun with a changed fallback file = %v, want ErrConflict", err)
	}
}

func containsPath(xs []string, slashPath string) bool {
	for _, x := range xs {
		if filepath.ToSlash(x) == slashPath {
			return true
		}
	}
	return false
}

// TestDryRunOfAResumeRemovesNothing: a dry run over a half-filled store
// reports the resume and leaves the interrupted run's temp file in place; the
// temp file goes only in the real run, under the lock.
func TestDryRunOfAResumeRemovesNothing(t *testing.T) {
	old := oldStore(t)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	linux := platform.Linux
	if _, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(newRoot, "mappings.json")); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(newRoot, "configs", ".tycswap-migrate-123.tmp")
	write(t, tmp, "half", 0o600)
	before := snapshot(t, newRoot)

	rep, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, DryRun: true, Platform: &linux})
	if err != nil || !rep.Resumed {
		t.Fatalf("dry run = %+v, %v; want a resumed report", rep, err)
	}
	if !equalMaps(before, snapshot(t, newRoot)) {
		t.Error("the dry run changed the new store")
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("the dry run removed the temp file: %v", err)
	}

	if _, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("the real run kept the temp file")
	}
	if _, err := os.Stat(filepath.Join(newRoot, "mappings.json")); err != nil {
		t.Errorf("the real run did not copy the missing file: %v", err)
	}
}

// TestResumeRestoresTheStoreModes: a rerun gives a verified file 0600 and a
// verified directory 0700 again, the modes a fresh copy writes; a dry run
// leaves them.
func TestResumeRestoresTheStoreModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX modes")
	}
	old := oldStore(t)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	linux := platform.Linux
	if _, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux}); err != nil {
		t.Fatal(err)
	}
	seq := filepath.Join(newRoot, "sequence.json")
	cfg := filepath.Join(newRoot, "configs")
	for p, m := range map[string]os.FileMode{seq: 0o644, cfg: 0o755} {
		if err := os.Chmod(p, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, DryRun: true, Platform: &linux}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(seq); fi.Mode().Perm() != 0o644 {
		t.Errorf("dry run changed the file mode to %v", fi.Mode().Perm())
	}
	rep, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &linux})
	if err != nil || !rep.Resumed {
		t.Fatalf("rerun = %+v, %v", rep, err)
	}
	if fi, _ := os.Stat(seq); fi.Mode().Perm() != 0o600 {
		t.Errorf("verified file mode = %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o700 {
		t.Errorf("verified dir mode = %v, want 0700", fi.Mode().Perm())
	}
}

// deniedGetKC is a Keychain whose reads of one service fail, as a denied or
// timed-out `security find-generic-password` does.
type deniedGetKC struct {
	*keychain.Fake
	service string
}

func (d *deniedGetKC) Get(service, account string) (string, bool, error) {
	if service == d.service {
		return "", false, &keychain.KeychainError{Msg: "denied"}
	}
	return d.Fake.Get(service, account)
}

// TestKeychainReadErrorOnTheNewServiceStopsTheCopy: when the new service
// cannot be read, migrate refuses with the error instead of writing over an
// item it could not see.
func TestKeychainReadErrorOnTheNewServiceStopsTheCopy(t *testing.T) {
	old := oldStore(t)
	newRoot := filepath.Join(t.TempDir(), "tycswap")
	base := keychain.NewFake()
	base.Seed(keychain.OldBackupService, "account-1-a@example.com", "blob")
	base.Seed(keychain.BackupService, "account-1-a@example.com", "newer, unseen")
	kc := &deniedGetKC{Fake: base, service: keychain.BackupService}
	mac := platform.MacOS

	_, err := Run(Options{NewRoot: newRoot, OldRoots: []string{old}, Platform: &mac, Keychain: kc})
	if err == nil || !strings.Contains(err.Error(), "read Keychain item "+keychain.BackupService+"/account-1-a@example.com") {
		t.Fatalf("Run = %v, want the read error naming the item", err)
	}
	if v, _, _ := base.Get(keychain.BackupService, "account-1-a@example.com"); v != "newer, unseen" {
		t.Errorf("the unreadable item was overwritten: %q", v)
	}
}
