// hardening_test.go — Import's identity check, alias normalisation and size
// limit, and Purge's symlink-aware root guard.

package transfer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.dpemmons.com/dpemmons/cswap/internal/codex/authfile"
	"git.dpemmons.com/dpemmons/cswap/internal/testutil"
)

// jwtAuth is an auth.json whose unsigned JWT decodes to (user, acct).
func jwtAuth(user, acct, email string) map[string]any {
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	tok := seg(map[string]any{"alg": "none"}) + "." + seg(map[string]any{
		"exp": 4102444800, "email": email,
		authfile.AuthClaim: map[string]any{"chatgpt_account_id": acct, "chatgpt_user_id": user},
	}) + ".sig"
	return map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]any{"id_token": tok, "access_token": tok, "refresh_token": "rt", "account_id": acct},
	}
}

func (e *env) importDoc(rows ...map[string]any) int {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"version": 1, "provider": "codex", "accounts": rows})
	path := filepath.Join(e.dir, "export.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		e.t.Fatal(err)
	}
	n, err := Import(e.open(), path, false, nil)
	if err != nil {
		e.t.Fatalf("Import: %v", err)
	}
	return n
}

func TestImportSkipsARowWhoseTokensBelongToAnotherAccount(t *testing.T) {
	e := newEnv(t)
	n := e.importDoc(
		map[string]any{"accountKey": keyA, "email": "a@x", "auth": jwtAuth("user-b", "acct-b", "b@x")},
		map[string]any{"accountKey": keyB, "email": "b@x", "auth": jwtAuth("user-b", "acct-b", "b@x")},
		// No decodable identity: accepted, as the Python accepts every row.
		map[string]any{"accountKey": keyC, "email": "c@x", "auth": authJSON("acct-c", "c@x")},
	)
	if n != 2 {
		t.Fatalf("imported %d, want 2", n)
	}
	st := e.open()
	if st.SlotForKey(keyA) != nil || st.ReadSnapshot(keyA) != nil {
		t.Fatal("mismatched row was imported")
	}
	if st.SlotForKey(keyB) == nil || st.SlotForKey(keyC) == nil {
		t.Fatalf("slots = %+v", st.Slots())
	}
}

func TestImportNormalisesAliasesAndDropsInvalidOnes(t *testing.T) {
	e := newEnv(t)
	e.importDoc(
		map[string]any{"accountKey": keyA, "alias": " Work ", "auth": authJSON("acct-a", "a@x")},
		map[string]any{"accountKey": keyB, "alias": "2", "auth": authJSON("acct-b", "b@x")},
		map[string]any{"accountKey": keyC, "alias": "-rm", "auth": authJSON("acct-c", "c@x")},
	)
	st := e.open()
	if got := st.SlotForKey(keyA).Alias; got != "work" {
		t.Fatalf("alias A = %q, want work", got)
	}
	for _, k := range []string{keyB, keyC} {
		sl := st.SlotForKey(k)
		if sl == nil || sl.Alias != "" {
			t.Fatalf("slot %s = %+v, want imported without alias", k, sl)
		}
	}
}

func TestImportRefusesAnOversizedDocument(t *testing.T) {
	e := newEnv(t)
	big := bytes.Repeat([]byte(" "), MaxImportBytes+1)
	_, err := Import(e.open(), "-", false, bytes.NewReader(big))
	if err == nil || !strings.Contains(err.Error(), "larger than 8 MiB") {
		t.Fatalf("stdin Import = %v, want size refusal", err)
	}
	path := filepath.Join(e.dir, "big.json")
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(e.open(), path, false, nil); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("file Import = %v, want size refusal", err)
	}
}

func TestPurgeRefusesACodexHomeReachedThroughASymlinkIntoTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Join(e.root, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(e.dir, "link")
	if err := os.Symlink(e.root, link); err != nil {
		t.Fatal(err)
	}
	testutil.Setenv(t, "CODEX_HOME", filepath.Join(link, "home"))
	var out bytes.Buffer
	ran, err := Purge(e.open(), true, nil, &out)
	if err == nil || ran {
		t.Fatalf("Purge = (%v, %v), want refusal", ran, err)
	}
	if _, err := os.Stat(e.root); err != nil {
		t.Fatalf("root removed: %v", err)
	}
	// A codex home that does not exist yet, under the link, is caught too.
	testutil.Setenv(t, "CODEX_HOME", filepath.Join(link, "later", "home"))
	if _, err := Purge(e.open(), true, nil, &out); err == nil {
		t.Fatal("Purge accepted a not-yet-existing codex home under the root")
	}
	// A sibling whose name merely extends the root's is not inside it.
	testutil.Setenv(t, "CODEX_HOME", e.root+"x")
	if err := guardRoot(e.root); err != nil {
		t.Fatalf("guardRoot with sibling home = %v", err)
	}
}
