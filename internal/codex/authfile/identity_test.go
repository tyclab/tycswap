// identity_test.go — parsing ~/.codex/auth.json into an identity tycswap can
// match to a slot, and writing it back. Ports claude-swap PR #252
// tests/test_codex_auth_file.py.

package authfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// codex-auth's format, kept byte-identical so imported keys match ours.
func TestAccountKey_JoinsUserAndAccount(t *testing.T) {
	if got, want := AccountKey(testUser, testAccount), testUser+"::"+testAccount; got != want {
		t.Fatalf("AccountKey = %q, want %q", got, want)
	}
}

// Verified against a real codex-auth snapshot filename.
func TestFileKey_IsUnpaddedBase64URLOfTheAccountKey(t *testing.T) {
	want := "dXNlci1LNlhDQ1d3NGdjUnBmYUdSNlZRS0FGZ0E6OjJmNGRhYzhmLWYxNWYtNGM1OC1hNTY3" +
		"LWU5Njk4NWQ1MWNmZA"
	if got := FileKey(AccountKey(testUser, testAccount)); got != want {
		t.Fatalf("FileKey = %q, want %q", got, want)
	}
}

func TestParseIdentity_ComesFromTheJWTClaims(t *testing.T) {
	ident := ParseIdentity(makeAuthJSON(t, authOpts{Email: "me@example.com"}))
	if ident == nil {
		t.Fatal("ParseIdentity = nil")
	}
	want := Identity{AccountID: testAccount, UserID: testUser, Email: "me@example.com", Plan: "pro"}
	if *ident != want {
		t.Fatalf("ParseIdentity = %+v, want %+v", *ident, want)
	}
	if got := ident.AccountKey(); got != testUser+"::"+testAccount {
		t.Fatalf("AccountKey() = %q", got)
	}
	if !ident.Identifiable() {
		t.Fatal("a ChatGPT login must be identifiable")
	}
}

// The live file's tokens.account_id is what the codex CLI itself uses to pick
// a workspace, so it is the authority when the two disagree.
func TestParseIdentity_TokensAccountIDWinsOverTheJWTClaim(t *testing.T) {
	payload := makeAuthJSON(t, authOpts{})
	payload["tokens"].(map[string]any)["account_id"] = "other-account"
	if got := ParseIdentity(payload).AccountID; got != "other-account" {
		t.Fatalf("AccountID = %q, want other-account", got)
	}
}

// Rule 2: with no tokens.account_id the JWT's chatgpt_account_id is used.
func TestParseIdentity_JWTClaimWhenTokensAccountIDIsAbsent(t *testing.T) {
	payload := makeAuthJSON(t, authOpts{})
	delete(payload["tokens"].(map[string]any), "account_id")
	if got := ParseIdentity(payload).AccountID; got != testAccount {
		t.Fatalf("AccountID = %q, want %q", got, testAccount)
	}
}

// Phone-login auth files carry neither tokens.account_id nor the account
// claim; codex-auth falls back to the default organization, and so do we.
func TestParseIdentity_OrgFallback(t *testing.T) {
	cases := []struct {
		name string
		orgs []any
		want string
	}{
		{"prefers the default org", []any{
			map[string]any{"id": "org-first", "is_default": false},
			map[string]any{"id": "org-default", "is_default": true},
		}, "org-default"},
		{"takes the first when none is default", []any{
			map[string]any{"id": "org-first"},
			map[string]any{"id": "org-second"},
		}, "org-first"},
		{"skips orgs with an empty id", []any{
			"not-an-org",
			map[string]any{"id": "", "is_default": true},
			map[string]any{"id": "org-real"},
		}, "org-real"},
		{"no orgs leaves the account unidentifiable", []any{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := stripAccountClaim(t, makeAuthJSON(t, authOpts{Organizations: c.orgs}))
			ident := ParseIdentity(payload)
			if ident == nil {
				t.Fatal("ParseIdentity = nil")
			}
			if ident.AccountID != c.want {
				t.Fatalf("AccountID = %q, want %q", ident.AccountID, c.want)
			}
			if ident.Identifiable() != (c.want != "") {
				t.Fatalf("Identifiable() = %v", ident.Identifiable())
			}
		})
	}
}

// auth_mode=apikey has no OAuth tokens: neither usage nor refresh applies, and
// it must not crash the parser.
func TestParseIdentity_APIKeyAccountYieldsAnIdentityWithNoTokens(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"auth_mode apikey with null tokens", map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x", "tokens": nil}},
		{"bare OPENAI_API_KEY", map[string]any{"OPENAI_API_KEY": "sk-x"}},
		{"auth_mode apikey alone", map[string]any{"auth_mode": "apikey"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ident := ParseIdentity(c.payload)
			if ident == nil {
				t.Fatal("ParseIdentity = nil")
			}
			if !ident.IsAPIKey || ident.AccountID != "" || ident.Identifiable() {
				t.Fatalf("ParseIdentity = %+v, want an unidentifiable API-key identity", *ident)
			}
		})
	}
}

// An apikey auth_mode alongside decodable tokens is still flagged.
func TestParseIdentity_APIKeyModeWithTokensIsFlagged(t *testing.T) {
	payload := makeAuthJSON(t, authOpts{})
	payload["auth_mode"] = "apikey"
	if ident := ParseIdentity(payload); ident == nil || !ident.IsAPIKey || ident.AccountID != testAccount {
		t.Fatalf("ParseIdentity = %+v", ident)
	}
}

func TestParseIdentity_UnparseablePayloadYieldsNil(t *testing.T) {
	cases := []struct {
		name    string
		payload any
	}{
		{"id token is not a JWT", map[string]any{"tokens": map[string]any{"id_token": "not-a-jwt"}}},
		{"empty object", map[string]any{}},
		{"not an object", "nonsense"},
		{"nil", nil},
		{"empty API key is not an API-key login", map[string]any{"OPENAI_API_KEY": ""}},
		{"claims are not an object", map[string]any{"tokens": map[string]any{"id_token": "a.WzFd.sig"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseIdentity(c.payload); got != nil {
				t.Fatalf("ParseIdentity = %+v, want nil", *got)
			}
		})
	}
}

// Python's `decode(id) or decode(access)`: an unreadable id token falls back
// to the access token.
func TestParseIdentity_FallsBackToTheAccessToken(t *testing.T) {
	payload := makeAuthJSON(t, authOpts{Email: "access@example.com"})
	payload["tokens"].(map[string]any)["id_token"] = "garbage"
	if ident := ParseIdentity(payload); ident == nil || ident.Email != "access@example.com" {
		t.Fatalf("ParseIdentity = %+v", ident)
	}
}

func TestDecodeJWTClaims(t *testing.T) {
	good := makeJWT(t, map[string]any{"email": "x@example.com"})
	cases := []struct {
		name  string
		token any
		ok    bool
	}{
		{"valid", good, true},
		{"padded segment", "a.eyJhIjoxfQ==.s", true},
		{"not a string", 42, false},
		{"single segment", "abc", false},
		{"bad base64", "a.!!!.s", false},
		{"not json", "a.bm90IGpzb24.s", false},
		{"json array", "a.WzFd.s", false},
		{"invalid utf-8", "a._w.s", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DecodeJWTClaims(c.token); (got != nil) != c.ok {
				t.Fatalf("DecodeJWTClaims(%v) = %v, want ok=%v", c.token, got, c.ok)
			}
		})
	}
}

func TestReadLiveIdentity_NilWhenTheFileIsAbsent(t *testing.T) {
	codexHome(t)
	if got := ReadLiveIdentity(); got != nil {
		t.Fatalf("ReadLiveIdentity = %+v, want nil", *got)
	}
}

func TestReadLiveIdentity_ReadsTheLiveFile(t *testing.T) {
	writeLiveAuth(t, codexHome(t), makeAuthJSON(t, authOpts{}))
	if ident := ReadLiveIdentity(); ident == nil || ident.AccountID != testAccount {
		t.Fatalf("ReadLiveIdentity = %+v", ident)
	}
}

// A half-written auth.json is transient, not fatal — the next pass re-reads.
func TestReadLiveIdentity_SurvivesATornWrite(t *testing.T) {
	dir := codexHome(t)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens": {`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadLiveIdentity(); got != nil {
		t.Fatalf("ReadLiveIdentity = %+v, want nil", *got)
	}
}

func TestReadLivePayload_RejectsNonObjects(t *testing.T) {
	dir := codexHome(t)
	for _, body := range []string{`[1, 2]`, `"x"`, `{} {}`, "{\"a\": \"\xff\"}"} {
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := ReadLivePayload(); got != nil {
			t.Fatalf("ReadLivePayload(%q) = %v, want nil", body, got)
		}
	}
}

// The file carries bearer tokens: an existing auth.json keeps its owner bits
// but never stays group- or world-readable once tycswap has written tokens to it.
func TestWriteLiveAuth_DropsGroupAndWorldBitsFromTheExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are not meaningful on Windows")
	}
	for _, tc := range []struct{ before, want os.FileMode }{
		{0o644, 0o600}, {0o666, 0o600}, {0o600, 0o600}, {0o400, 0o400}, {0o044, 0o600},
	} {
		dir := codexHome(t)
		path := writeLiveAuth(t, dir, makeAuthJSON(t, authOpts{}))
		if err := os.Chmod(path, tc.before); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteLiveAuth(makeAuthJSON(t, authOpts{Email: "b@example.com"})); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != tc.want {
			t.Errorf("mode %o -> %o, want %o", tc.before, got, tc.want)
		}
	}
}

// The codex home is the codex CLI's directory: writing auth.json into it must
// not tighten it to atomicfile's 0700 default.
func TestWriteLiveAuth_PreservesTheCodexHomeMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are not meaningful on Windows")
	}
	dir := codexHome(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteLiveAuth(makeAuthJSON(t, authOpts{})); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Fatalf("codex home mode = %o, want 755", got)
	}
}

func TestWriteLiveAuth_CreatesAPrivateFileWhenAbsent(t *testing.T) {
	dir := codexHome(t)
	path, err := WriteLiveAuth(makeAuthJSON(t, authOpts{}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "auth.json"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

// A missing codex home is created rather than failing the write.
func TestWriteLiveAuth_CreatesTheCodexHome(t *testing.T) {
	home := isolate(t)
	if _, err := WriteLiveAuth(makeAuthJSON(t, authOpts{})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(filepath.Join(home, ".codex"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Fatalf("created codex home mode = %o, want 700", got)
	}
}

func TestWriteLiveAuth_RoundTripsThePayload(t *testing.T) {
	codexHome(t)
	payload := makeAuthJSON(t, authOpts{Email: "round@example.com"})
	if _, err := WriteLiveAuth(payload); err != nil {
		t.Fatal(err)
	}
	if got := ReadLivePayload(); !reflect.DeepEqual(got, payload) {
		t.Fatalf("round trip:\n got %v\nwant %v", got, payload)
	}
}

// Integers survive a read/write cycle exactly (json.Number, not float64).
func TestWriteLiveAuth_RoundTripsIntegersExactly(t *testing.T) {
	dir := codexHome(t)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"n": 9007199254740993}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteLiveAuth(ReadLivePayload()); err != nil {
		t.Fatal(err)
	}
	if got := ReadLivePayload()["n"]; got != json.Number("9007199254740993") {
		t.Fatalf("n = %v", got)
	}
}

func TestWriteLiveAuth_LeavesNoTempFileBehind(t *testing.T) {
	dir := codexHome(t)
	if _, err := WriteLiveAuth(makeAuthJSON(t, authOpts{})); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	hidden, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if len(matches)+len(hidden) != 0 {
		t.Fatalf("temp files left behind: %v %v", matches, hidden)
	}
}

func TestAccessTokenExpiry_IsReadFromTheAccessToken(t *testing.T) {
	got := AccessTokenExpiry(makeAuthJSON(t, authOpts{Exp: 1_800_000_000}))
	if got == nil || *got != 1_800_000_000 {
		t.Fatalf("AccessTokenExpiry = %v, want 1800000000", got)
	}
}

func TestAccessTokenExpiry_NilWhenUnreadable(t *testing.T) {
	cases := []struct {
		name    string
		payload any
	}{
		{"access token is not a JWT", map[string]any{"tokens": map[string]any{"access_token": "x"}}},
		{"empty object", map[string]any{}},
		{"not an object", "x"},
		{"exp is not a number", map[string]any{"tokens": map[string]any{
			"access_token": makeJWT(t, map[string]any{"exp": "soon"}),
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AccessTokenExpiry(c.payload); got != nil {
				t.Fatalf("AccessTokenExpiry = %v, want nil", *got)
			}
		})
	}
}
