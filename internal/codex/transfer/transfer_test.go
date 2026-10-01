// transfer_test.go — Codex export/import/purge and ResolveSlot. Ports the
// export/import/purge cases of claude-swap PR #252 tests/test_codex_parity.py
// and the resolve_account cases of tests/test_codex_switcher.py.
//
// Every store is rooted in t.TempDir() with a keychain.Fake, and HOME and
// CODEX_HOME point at temp dirs, so nothing reaches the real ~/.codex.

package transfer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
)

var (
	keyA = authfile.AccountKey("user-a", "acct-a")
	keyB = authfile.AccountKey("user-b", "acct-b")
	keyC = authfile.AccountKey("user-c", "acct-c")
)

type env struct {
	t         *testing.T
	dir       string
	codexHome string
	root      string
	kc        *keychain.Fake
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	e := &env{t: t, dir: base, codexHome: filepath.Join(base, ".codex"),
		root: filepath.Join(base, "backup", "codex"), kc: keychain.NewFake()}
	testutil.Setenv(t, "HOME", base)
	testutil.Setenv(t, "CODEX_HOME", e.codexHome)
	return e
}

func (e *env) open() *store.Store {
	return store.New(store.Options{Root: e.root, Keychain: e.kc, Platform: platform.Linux})
}

func authJSON(acct, email string) map[string]any {
	return map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens": map[string]any{"id_token": "id." + email, "access_token": "at." + email,
			"refresh_token": "rt-" + acct, "account_id": acct},
		"last_refresh": "2026-08-16T00:00:00Z",
	}
}

// seeded is the Python fixture: three accounts, A live.
func seeded(t *testing.T) *env {
	e := newEnv(t)
	st := e.open()
	for _, r := range []struct{ key, acct, email string }{
		{keyA, "acct-a", "a@x.io"}, {keyB, "acct-b", "b@x.io"}, {keyC, "acct-c", "c@x.io"},
	} {
		if _, err := st.UpsertSlot(r.key, store.Upsert{Email: r.email, Plan: "pro"}); err != nil {
			t.Fatal(err)
		}
		if err := st.WriteSnapshot(r.key, authJSON(r.acct, r.email)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetActive(keyA); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(authJSON("acct-a", "a@x.io"))
	if err := os.WriteFile(filepath.Join(e.codexHome, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) export(account string) string {
	e.t.Helper()
	target := filepath.Join(e.dir, "out", "codex.json")
	if _, err := Export(e.open(), target, account, nil); err != nil {
		e.t.Fatalf("Export: %v", err)
	}
	return target
}

func (e *env) purge() {
	e.t.Helper()
	if _, err := Purge(e.open(), true, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		e.t.Fatalf("Purge: %v", err)
	}
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func wantTransferErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("err = %v, want one containing %q", err, substr)
	}
	if cerr.TypeName(err) != string(cerr.KindTransfer) {
		t.Fatalf("kind = %q, want TransferError", cerr.TypeName(err))
	}
}

// ---- resolve ----------------------------------------------------------

func TestResolveSlotAcceptsNumberEmailAndAlias(t *testing.T) {
	e := seeded(t)
	if err := e.open().SetAlias(keyB, "work"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"2", "b@x.io", "B@X.IO", "work", "WORK", "  work  "} {
		sl, err := ResolveSlot(e.open(), id)
		if err != nil || sl.Number != "2" {
			t.Fatalf("ResolveSlot(%q) = %+v, %v", id, sl, err)
		}
	}
}

func TestResolveSlotRejectsAnEmptyIdentifier(t *testing.T) {
	// An empty needle must not match the empty alias every slot starts with.
	e := seeded(t)
	for _, id := range []string{"", "   "} {
		if _, err := ResolveSlot(e.open(), id); !cerr.IsClaudeSwitchError(err) {
			t.Fatalf("ResolveSlot(%q) err = %v", id, err)
		}
	}
}

func TestResolveSlotErrorText(t *testing.T) {
	e := seeded(t)
	_, err := ResolveSlot(e.open(), "nobody")
	if err == nil || err.Error() != "No Codex account matches 'nobody'" {
		t.Fatalf("err = %v", err)
	}
}

// ---- export -----------------------------------------------------------

func TestExportWritesEveryAccountWithItsCredentials(t *testing.T) {
	e := seeded(t)
	target := filepath.Join(e.dir, "codex.json")
	n, err := Export(e.open(), target, "", nil)
	if err != nil || n != 3 {
		t.Fatalf("Export = %d, %v", n, err)
	}
	doc := readDoc(t, target)
	if doc["provider"] != "codex" || doc["version"] != float64(1) {
		t.Fatalf("header = %v %v", doc["provider"], doc["version"])
	}
	accts := doc["accounts"].([]any)
	if len(accts) != 3 {
		t.Fatalf("accounts = %d", len(accts))
	}
	first := accts[0].(map[string]any)
	if first["auth"].(map[string]any)["tokens"].(map[string]any)["refresh_token"] == "" {
		t.Fatal("no refresh token")
	}
	var keys []string
	for k := range first {
		keys = append(keys, k)
	}
	if len(keys) != 8 {
		t.Fatalf("row fields = %v", keys)
	}
	for _, k := range []string{"accountKey", "email", "plan", "workspaceName", "alias", "disabled", "authMode", "auth"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
}

func TestTheExportWarnsThatItHoldsLiveTokens(t *testing.T) {
	e := seeded(t)
	if w, _ := readDoc(t, e.export(""))["warning"].(string); !strings.Contains(w, "live OAuth tokens") || w != Warning {
		t.Fatalf("warning = %q", w)
	}
}

func TestTheExportFileIsCreatedPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	e := seeded(t)
	target := filepath.Join(e.dir, "codex.json")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{e.export(""), target} {
		if p == target {
			if _, err := Export(e.open(), target, "", nil); err != nil {
				t.Fatal(err)
			}
		}
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v", p, fi.Mode().Perm(), err)
		}
	}
}

func TestExportCanBeLimitedToOneAccount(t *testing.T) {
	e := seeded(t)
	target := filepath.Join(e.dir, "one.json")
	n, err := Export(e.open(), target, "2", nil)
	if err != nil || n != 1 {
		t.Fatalf("Export = %d, %v", n, err)
	}
	if email := readDoc(t, target)["accounts"].([]any)[0].(map[string]any)["email"]; email != "b@x.io" {
		t.Fatalf("email = %v", email)
	}
}

func TestExportOfAnUnknownAccountFails(t *testing.T) {
	e := seeded(t)
	_, err := Export(e.open(), filepath.Join(e.dir, "x.json"), "zzz", nil)
	if err == nil || !strings.Contains(err.Error(), "No Codex account matches 'zzz'") {
		t.Fatalf("err = %v", err)
	}
}

func TestExportToStdout(t *testing.T) {
	e := seeded(t)
	var out bytes.Buffer
	if _, err := Export(e.open(), "-", "", &out); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["provider"] != "codex" {
		t.Fatalf("stdout doc = %v, %v", doc, err)
	}
	if !bytes.HasSuffix(out.Bytes(), []byte("}\n")) {
		t.Fatal("print() adds one newline")
	}
}

func TestExportOfAnEmptyStoreFails(t *testing.T) {
	e := newEnv(t)
	_, err := Export(e.open(), "-", "", &bytes.Buffer{})
	wantTransferErr(t, err, "No Codex accounts to export")
}

func TestExportSkipsSlotsWithoutCredentials(t *testing.T) {
	e := newEnv(t)
	if _, err := e.open().UpsertSlot(keyA, store.Upsert{Email: "a@x.io"}); err != nil {
		t.Fatal(err)
	}
	_, err := Export(e.open(), "-", "", &bytes.Buffer{})
	wantTransferErr(t, err, "No Codex accounts with stored credentials to export")
}

// ---- import -----------------------------------------------------------

func TestImportRestoresAccountsIntoAnEmptyStore(t *testing.T) {
	e := seeded(t)
	target := e.export("")
	e.purge()
	if n := len(e.open().Slots()); n != 0 {
		t.Fatalf("slots after purge = %d", n)
	}
	n, err := Import(e.open(), target, false, nil)
	if err != nil || n != 3 {
		t.Fatalf("Import = %d, %v", n, err)
	}
	st := e.open()
	if len(st.Slots()) != 3 {
		t.Fatalf("slots = %d", len(st.Slots()))
	}
	if id := st.ReadSnapshot(keyA)["tokens"].(map[string]any)["account_id"]; id != "acct-a" {
		t.Fatalf("account_id = %v", id)
	}
}

func TestImportCarriesAliasesAndDisabledFlags(t *testing.T) {
	e := seeded(t)
	st := e.open()
	if err := st.SetAlias(keyB, "work"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDisabled(keyC, true); err != nil {
		t.Fatal(err)
	}
	target := e.export("")
	e.purge()
	if _, err := Import(e.open(), target, false, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]store.Slot{}
	for _, s := range e.open().Slots() {
		got[s.AccountKey] = s
	}
	if got[keyB].Alias != "work" || !got[keyC].Disabled {
		t.Fatalf("restored = %+v", got)
	}
}

func TestImportSkipsExistingAccountsUnlessForced(t *testing.T) {
	e := seeded(t)
	target := e.export("")
	if n, err := Import(e.open(), target, false, nil); err != nil || n != 0 {
		t.Fatalf("unforced = %d, %v", n, err)
	}
	if n, err := Import(e.open(), target, true, nil); err != nil || n != 3 {
		t.Fatalf("forced = %d, %v", n, err)
	}
	if n := len(e.open().Slots()); n != 3 {
		t.Fatalf("slots = %d, want 3 (upsert)", n)
	}
}

func TestImportFromStdin(t *testing.T) {
	e := seeded(t)
	var out bytes.Buffer
	if _, err := Export(e.open(), "-", "", &out); err != nil {
		t.Fatal(err)
	}
	e.purge()
	if n, err := Import(e.open(), "-", false, &out); err != nil || n != 3 {
		t.Fatalf("Import(-) = %d, %v", n, err)
	}
}

func TestImportRefusals(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"claude export", `{"provider":"claude","accounts":[{}]}`, "not a Codex one"},
		{"newer version", `{"provider":"codex","version":99,"accounts":[{}]}`, "newer than"},
		{"junk", `{ not json`, "not valid JSON"},
		{"non-object", `[1]`, "is not a tycswap export"},
		{"no accounts", `{"provider":"codex","version":1,"accounts":[]}`, "contains no accounts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			p := filepath.Join(e.dir, "in.json")
			if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Import(e.open(), p, false, nil)
			wantTransferErr(t, err, c.want)
			if n := len(e.open().Slots()); n != 0 {
				t.Fatalf("slots = %d, want nothing half-applied", n)
			}
		})
	}
}

func TestImportOfAMissingFileIsACleanError(t *testing.T) {
	e := newEnv(t)
	_, err := Import(e.open(), filepath.Join(e.dir, "nope.json"), false, nil)
	wantTransferErr(t, err, "Cannot read")
}

func TestImportSkipsMalformedRows(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "in.json")
	body := `{"accounts":[1,{"accountKey":""},{"accountKey":"k","auth":"x"},{"accountKey":"` + keyA + `","auth":{"n":12345678901234567890}}]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := Import(e.open(), p, false, nil)
	if err != nil || n != 1 {
		t.Fatalf("Import = %d, %v", n, err)
	}
	if s := e.open().Slots()[0]; s.AuthMode != "chatgpt" {
		t.Fatalf("authMode = %q, want the chatgpt default", s.AuthMode)
	}
	b, _ := os.ReadFile(filepath.Join(e.root, "credentials", authfile.FileKey(keyA)+".json"))
	if !strings.Contains(string(b), "12345678901234567890") {
		t.Fatalf("snapshot numbers not verbatim: %s", b)
	}
}

// ---- purge ------------------------------------------------------------

func TestPurgeRemovesSlotsAndSnapshots(t *testing.T) {
	e := seeded(t)
	var out bytes.Buffer
	ran, err := Purge(e.open(), true, nil, &out)
	if err != nil || !ran {
		t.Fatalf("Purge = %v, %v", ran, err)
	}
	st := e.open()
	if len(st.Slots()) != 0 || st.ReadSnapshot(keyA) != nil {
		t.Fatal("slots or snapshot left behind")
	}
	if _, err := os.Stat(e.root); !os.IsNotExist(err) {
		t.Fatalf("root still exists: %v", err)
	}
	if !strings.Contains(out.String(), "Removed 3 Codex account(s) and "+e.root) {
		t.Fatalf("out = %q", out.String())
	}
}

func TestPurgeRemovesKeychainItems(t *testing.T) {
	e := seeded(t)
	st := store.New(store.Options{Root: e.root, Keychain: e.kc, Platform: platform.MacOS})
	if err := st.WriteSnapshot(keyA, authJSON("acct-a", "a@x.io")); err != nil {
		t.Fatal(err)
	}
	if _, err := Purge(st, true, nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if e.kc.Exists(store.KeychainService, authfile.FileKey(keyA)) {
		t.Fatal("keychain item left behind")
	}
}

func TestPurgeLeavesTheLiveCodexLoginAlone(t *testing.T) {
	// tycswap manages copies; the user's actual ~/.codex login is not ours.
	e := seeded(t)
	live := filepath.Join(e.codexHome, "auth.json")
	before, _ := os.ReadFile(live)
	e.purge()
	after, err := os.ReadFile(live)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("live login changed: %v", err)
	}
}

func TestPurgeRefusesARootContainingTheCodexHome(t *testing.T) {
	e := seeded(t)
	for _, root := range []string{e.codexHome, e.dir} {
		st := store.New(store.Options{Root: root, Keychain: e.kc, Platform: platform.Linux})
		if _, err := Purge(st, true, nil, &bytes.Buffer{}); err == nil {
			t.Fatalf("Purge(%s) ran", root)
		}
	}
	if _, err := os.Stat(filepath.Join(e.codexHome, "auth.json")); err != nil {
		t.Fatalf("live login gone: %v", err)
	}
}

func TestPurgeAsksFirst(t *testing.T) {
	for _, answer := range []string{"n\n", "", "yes\n", "y \n"} {
		e := seeded(t)
		var out bytes.Buffer
		ran, err := Purge(e.open(), false, strings.NewReader(answer), &out)
		if err != nil || ran {
			t.Fatalf("answer %q: ran=%v err=%v", answer, ran, err)
		}
		if n := len(e.open().Slots()); n != 3 {
			t.Fatalf("answer %q: slots = %d", answer, n)
		}
		if !strings.Contains(out.String(), "[y/N] ") || !strings.Contains(out.String(), "Cancelled") {
			t.Fatalf("out = %q", out.String())
		}
	}
}

func TestPurgeProceedsOnYes(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\r\n", "y"} {
		e := seeded(t)
		ran, err := Purge(e.open(), false, strings.NewReader(answer), &bytes.Buffer{})
		if err != nil || !ran || len(e.open().Slots()) != 0 {
			t.Fatalf("answer %q: ran=%v err=%v", answer, ran, err)
		}
	}
}

func TestPurgeOnAnEmptyStoreSaysSo(t *testing.T) {
	e := newEnv(t)
	var out bytes.Buffer
	ran, err := Purge(e.open(), true, nil, &out)
	if err != nil || ran || !strings.Contains(out.String(), "No tycswap Codex data") {
		t.Fatalf("ran=%v err=%v out=%q", ran, err, out.String())
	}
}

// TestTheExportIsWrittenAtomically: the export replaces the destination by
// rename (a new inode, so a reader holding the old file never sees a mix) and
// leaves no temp file behind.
func TestTheExportIsWrittenAtomically(t *testing.T) {
	e := seeded(t)
	target := filepath.Join(e.dir, "out", "codex.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(target)
	if _, err := Export(e.open(), target, "", nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && os.SameFile(before, after) {
		t.Error("export wrote in place instead of renaming a temp file over the target")
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the export: %v", len(entries), entries)
	}
}
