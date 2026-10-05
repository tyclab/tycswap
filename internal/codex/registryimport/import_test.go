// import_test.go — the one-time codex-auth registry import. Ports claude-swap
// PR #252 tests/test_codex_import.py, plus the v3-only plan normalisation, the
// schema-selection edge cases and the injectable path defaults.
//
// Every test roots the store in t.TempDir() with a keychain.Fake and points
// CODEX_HOME at a temp tree, so nothing can reach the real ~/.codex.

package registryimport

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/codex/authfile"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
)

var (
	keyA = authfile.AccountKey("user-a", "acct-a")
	keyB = authfile.AccountKey("user-b", "acct-b")
)

type env struct {
	t         *testing.T
	codexHome string
	accounts  string
	root      string
	kc        *keychain.Fake
}

// newEnv is the Python codex_home fixture: a temp CODEX_HOME and HOME, and a
// file-backed store under its own temp root.
func newEnv(t *testing.T) *env {
	t.Helper()
	base := testutil.IsolateHome(t)
	e := &env{
		t:         t,
		codexHome: filepath.Join(base, ".codex"),
		root:      filepath.Join(base, "backup", "codex"),
		kc:        keychain.NewFake(),
	}
	e.accounts = filepath.Join(e.codexHome, "accounts")
	testutil.Setenv(t, "CODEX_HOME", e.codexHome)
	return e
}

// open is `CodexStore()`: a fresh Store over the same root.
func (e *env) open() *store.Store {
	return store.New(store.Options{Root: e.root, Keychain: e.kc, Platform: platform.Linux})
}

func (e *env) run(onlyIfEmpty bool) Result {
	e.t.Helper()
	r, err := Import(e.open(), Options{OnlyIfEmpty: onlyIfEmpty})
	if err != nil {
		e.t.Fatalf("Import: %v", err)
	}
	return r
}

func (e *env) writeFile(name string, v any) string {
	e.t.Helper()
	if err := os.MkdirAll(e.accounts, 0o700); err != nil {
		e.t.Fatal(err)
	}
	var b []byte
	if s, ok := v.(string); ok {
		b = []byte(s)
	} else {
		var err error
		if b, err = json.Marshal(v); err != nil {
			e.t.Fatal(err)
		}
	}
	p := filepath.Join(e.accounts, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// writeRegistry is the Python _write_registry.
func (e *env) writeRegistry(accounts []map[string]any, schema int) {
	e.t.Helper()
	var active any
	if len(accounts) > 0 {
		active = accounts[0]["account_key"]
	}
	if accounts == nil {
		accounts = []map[string]any{}
	}
	e.writeFile("registry.json", map[string]any{
		"schema_version":     schema,
		"active_account_key": active,
		"accounts":           accounts,
	})
}

func (e *env) snapshot(key string, payload any) {
	e.t.Helper()
	if payload == nil {
		payload = makeAuthJSONFor(key, "a@example.com")
	}
	e.writeFile(authfile.FileKey(key)+".auth.json", payload)
}

func seed(key, email, plan string) map[string]any {
	user, acct := splitKey(key)
	return map[string]any{
		"account_key":        key,
		"chatgpt_account_id": acct,
		"chatgpt_user_id":    user,
		"email":              email,
		"alias":              "",
		"account_name":       nil,
		"plan":               plan,
		"auth_mode":          "chatgpt",
		"created_at":         0,
		"last_used_at":       0,
	}
}

func splitKey(key string) (string, string) {
	for i := 0; i+1 < len(key); i++ {
		if key[i:i+2] == "::" {
			return key[:i], key[i+2:]
		}
	}
	return key, ""
}

// makeAuthJSON is a trimmed conftest_codex.make_auth_json: an auth.json in the
// shape codex writes, with an unsigned JWT carrying the email.
// Its tokens decode to keyA's identity; makeAuthJSONFor names another key.
func makeAuthJSON(email string) map[string]any { return makeAuthJSONFor(keyA, email) }

// makeAuthJSONFor is makeAuthJSON whose tokens decode to key's identity, so
// the importer's identity check accepts it under that key.
func makeAuthJSONFor(key, email string) map[string]any {
	user, acct := splitKey(key)
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	tok := seg(map[string]any{"alg": "none"}) + "." + seg(map[string]any{
		"exp": 4102444800, "email": email,
		authfile.AuthClaim: map[string]any{"chatgpt_account_id": acct, "chatgpt_user_id": user, "chatgpt_plan_type": "pro"},
	}) + ".sig"
	return map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": tok, "access_token": tok, "refresh_token": "rt-a", "account_id": acct,
		},
		"last_refresh": "2026-08-16T00:00:00Z",
	}
}

// roundTrip normalises a payload through JSON so it compares against what
// ReadSnapshot returns.
func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(v)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestImportIsANoopWhenNoRegistryExists(t *testing.T) {
	e := newEnv(t)
	if r := e.run(false); !reflect.DeepEqual(r, Result{}) {
		t.Fatalf("got %+v, want zero Result (source None)", r)
	}
	if n := len(e.open().Slots()); n != 0 {
		t.Fatalf("slots = %d, want 0", n)
	}
}

func TestImportCreatesOneSlotPerAccount(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro"), seed(keyB, "b@x", "pro")}, 3)
	e.snapshot(keyA, makeAuthJSON("a@x"))
	e.snapshot(keyB, makeAuthJSONFor(keyB, "b@x"))

	r := e.run(false)
	if r.Imported != 2 {
		t.Fatalf("imported = %d, want 2", r.Imported)
	}
	if r.Source != filepath.Join(e.accounts, "registry.json") {
		t.Fatalf("source = %q", r.Source)
	}
	if !r.DidAnything() {
		t.Fatal("DidAnything = false")
	}
	var emails, nums []string
	for _, s := range e.open().Slots() {
		emails = append(emails, s.Email)
		nums = append(nums, s.Number)
	}
	if !reflect.DeepEqual(emails, []string{"a@x", "b@x"}) || !reflect.DeepEqual(nums, []string{"1", "2"}) {
		t.Fatalf("emails %v numbers %v", emails, nums)
	}
}

func TestImportCopiesTheSnapshots(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	payload := makeAuthJSON("a@x")
	e.snapshot(keyA, payload)

	e.run(false)

	if got := e.open().ReadSnapshot(keyA); !reflect.DeepEqual(got, roundTrip(t, payload)) {
		t.Fatalf("snapshot = %v", got)
	}
}

func TestImportKeepsSnapshotNumbersVerbatim(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	e.snapshot(keyA, `{"auth_mode":"chatgpt","tokens":null,"n":12345678901234567890}`)

	e.run(false)

	b, err := os.ReadFile(filepath.Join(e.root, "credentials", authfile.FileKey(keyA)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["n"]) != "12345678901234567890" {
		t.Fatalf("n = %s, want the literal kept", doc["n"])
	}
}

func TestImportRenamesLegacyPlans(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "team")}, 3)
	e.snapshot(keyA, nil)

	e.run(false)

	if p := e.open().Slots()[0].Plan; p != "business" {
		t.Fatalf("plan = %q, want business", p)
	}
}

func TestImportLeavesSchema4PlansAlone(t *testing.T) {
	// v4 already carries final semantics; renaming its "business" again would
	// mislabel the account one tier up.
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "business")}, 4)
	e.snapshot(keyA, nil)

	e.run(false)

	if p := e.open().Slots()[0].Plan; p != "business" {
		t.Fatalf("plan = %q, want business", p)
	}
}

func TestImportCarriesTheAliasAcross(t *testing.T) {
	e := newEnv(t)
	row := seed(keyA, "a@x", "pro")
	row["alias"] = "work"
	e.writeRegistry([]map[string]any{row}, 3)
	e.snapshot(keyA, nil)

	e.run(false)

	if a := e.open().Slots()[0].Alias; a != "work" {
		t.Fatalf("alias = %q", a)
	}
}

func TestImportCarriesTheWorkspaceNameAcross(t *testing.T) {
	e := newEnv(t)
	row := seed(keyA, "a@x", "team")
	row["account_name"] = "Workspace Alpha"
	e.writeRegistry([]map[string]any{row}, 3)
	e.snapshot(keyA, nil)

	e.run(false)

	if w := e.open().Slots()[0].WorkspaceName; w != "Workspace Alpha" {
		t.Fatalf("workspace = %q", w)
	}
}

func TestImportLeavesTheSourceUntouched(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	e.snapshot(keyA, nil)
	reg := filepath.Join(e.accounts, "registry.json")
	snap := filepath.Join(e.accounts, authfile.FileKey(keyA)+".auth.json")
	read := func() [2]string {
		a, _ := os.ReadFile(reg)
		b, _ := os.ReadFile(snap)
		return [2]string{string(a), string(b)}
	}
	before := read()
	entries, _ := os.ReadDir(e.accounts)

	e.run(false)

	if read() != before {
		t.Fatal("source files changed")
	}
	after, _ := os.ReadDir(e.accounts)
	if len(after) != len(entries) {
		t.Fatalf("accounts dir gained files: %d -> %d", len(entries), len(after))
	}
}

func TestAnAccountWithoutASnapshotIsSkippedNotFatal(t *testing.T) {
	// A registry row whose auth file was deleted is a real state on disk; it
	// must cost that one account, not the whole import.
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro"), seed(keyB, "b@x", "pro")}, 3)
	e.snapshot(keyA, nil)

	r := e.run(false)

	if r.Imported != 1 || r.Skipped != 1 {
		t.Fatalf("got (%d, %d), want (1, 1)", r.Imported, r.Skipped)
	}
	slots := e.open().Slots()
	if len(slots) != 1 || slots[0].AccountKey != keyA {
		t.Fatalf("slots = %+v", slots)
	}
}

func TestACorruptSnapshotIsSkippedNotFatal(t *testing.T) {
	cases := map[string]string{"torn": "{ torn", "non-object": "[1, 2]", "null": "null"}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
			e.snapshot(keyA, body)

			r := e.run(false)

			if r.Imported != 0 || r.Skipped != 1 {
				t.Fatalf("got (%d, %d), want (0, 1)", r.Imported, r.Skipped)
			}
		})
	}
}

func TestAnAPIKeyAccountImportsWithoutTokens(t *testing.T) {
	e := newEnv(t)
	row := seed(keyA, "a@x", "pro")
	row["auth_mode"] = "apikey"
	e.writeRegistry([]map[string]any{row}, 3)
	e.snapshot(keyA, map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x", "tokens": nil})

	r := e.run(false)

	if r.Imported != 1 {
		t.Fatalf("imported = %d", r.Imported)
	}
	if m := e.open().Slots()[0].AuthMode; m != "apikey" {
		t.Fatalf("auth mode = %q", m)
	}
}

func TestImportReadsTheLegacyV2EmailKeyedShape(t *testing.T) {
	// v2 keyed accounts by email and had no account_key at all.
	e := newEnv(t)
	e.writeFile("registry.json", map[string]any{
		"version":      2,
		"active_email": "a@x",
		"accounts":     map[string]any{"a@x": map[string]any{"email": "a@x", "plan": "pro"}},
	})

	r := e.run(false)

	if r.Skipped != 1 || r.Imported != 0 {
		t.Fatalf("got %+v, want 1 skipped 0 imported", r)
	}
}

func TestANewerSchemaIsRefusedRatherThanMisread(t *testing.T) {
	// Guessing at a format we do not know would corrupt the user's accounts.
	e := newEnv(t)
	e.writeFile("registry.json", map[string]any{"schema_version": 99, "accounts": []any{}})

	r := e.run(false)

	if r.Imported != 0 || r.UnsupportedSchema == nil || *r.UnsupportedSchema != 99 {
		t.Fatalf("got %+v, want unsupported 99", r)
	}
	if r.Source != "" {
		t.Fatalf("source = %q, want none", r.Source)
	}
}

func TestSchemaSelection(t *testing.T) {
	// `schema_version or version or 2`, then isinstance(int) and <= 4.
	cases := []struct {
		name        string
		registry    string
		wantImport  int
		unsupported *int
	}{
		{"schema_version wins", `{"schema_version":3,"version":99,"accounts":ROWS}`, 1, nil},
		{"zero falls through to version", `{"schema_version":0,"version":4,"accounts":ROWS}`, 1, nil},
		{"null falls through to default 2", `{"schema_version":null,"accounts":ROWS}`, 1, nil},
		{"float is not an int", `{"schema_version":3.0,"accounts":ROWS}`, 0, nil},
		{"string is not an int", `{"schema_version":"3","accounts":ROWS}`, 0, nil},
		{"version newer", `{"version":5,"accounts":ROWS}`, 0, intp(5)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			rowsJSON, _ := json.Marshal([]any{seed(keyA, "a@x", "pro")})
			reg := strings.ReplaceAll(c.registry, "ROWS", string(rowsJSON))
			e.writeFile("registry.json", reg)
			e.snapshot(keyA, nil)

			r := e.run(false)

			if r.Imported != c.wantImport || !reflect.DeepEqual(r.UnsupportedSchema, c.unsupported) {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func intp(n int) *int { return &n }

func TestSchema4IsAccepted(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 4)
	e.snapshot(keyA, nil)
	if r := e.run(false); r.Imported != 1 {
		t.Fatalf("imported = %d", r.Imported)
	}
}

func TestImportRecordsTheActiveAccount(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	e.snapshot(keyA, nil)
	e.run(false)
	if k := e.open().ActiveKey(); k != keyA {
		t.Fatalf("active = %q", k)
	}
}

func TestAnActiveKeyNamingASkippedAccountIsNotRecorded(t *testing.T) {
	// Pointing the active marker at a slot that failed to import would make
	// every later status read resolve to nothing.
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	e.run(false)
	if k := e.open().ActiveKey(); k != "" {
		t.Fatalf("active = %q, want none", k)
	}
}

func TestOnlyIfEmptyDoesNotRunTwice(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	e.snapshot(keyA, nil)
	e.run(false)
	if r := e.run(true); r.Imported != 0 || r.Source != "" {
		t.Fatalf("got %+v, want a no-op", r)
	}
}

func TestOnlyIfEmptyRunsOnAnEmptyStore(t *testing.T) {
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	e.snapshot(keyA, nil)
	if r := e.run(true); r.Imported != 1 {
		t.Fatalf("imported = %d", r.Imported)
	}
}

func TestAnExplicitImportCanBeReRun(t *testing.T) {
	// `tycswap codex import-codex-auth` is a recovery path; it must not
	// silently no-op.
	e := newEnv(t)
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	e.snapshot(keyA, nil)
	e.run(false)
	if r := e.run(false); r.Imported != 1 {
		t.Fatalf("imported = %d", r.Imported)
	}
	if n := len(e.open().Slots()); n != 1 {
		t.Fatalf("slots = %d, want 1 (upsert, not duplicate)", n)
	}
}

func TestExplicitPathsOverrideTheDefaults(t *testing.T) {
	e := newEnv(t)
	other := t.TempDir()
	e.accounts = other
	e.writeRegistry([]map[string]any{seed(keyA, "a@x", "pro")}, 3)
	e.snapshot(keyA, nil)

	r, err := Import(e.open(), Options{
		RegistryPath: filepath.Join(other, "registry.json"),
		AccountsDir:  other,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Imported != 1 || r.Source != filepath.Join(other, "registry.json") {
		t.Fatalf("got %+v", r)
	}
}

func TestNonObjectRowsAndRegistriesAreIgnored(t *testing.T) {
	e := newEnv(t)
	e.writeFile("registry.json", `[1,2,3]`)
	if r := e.run(false); !reflect.DeepEqual(r, Result{}) {
		t.Fatalf("non-object registry: %+v", r)
	}
	e.writeFile("registry.json", `{"schema_version":3,"accounts":[1,"x",{"email":"no-key"}]}`)
	if r := e.run(false); r.Skipped != 1 || r.Imported != 0 {
		t.Fatalf("got %+v, want only the keyless object row counted", r)
	}
}
