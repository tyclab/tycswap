package ccfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/tyclab/tycswap/internal/ccfile"
)

// --- KeychainServiceName (spec 06§1.5/§6, DESIGN §5 WP4: NFC order-insensitive) ---

func TestKeychainServiceName_Deterministic(t *testing.T) {
	a := ccfile.KeychainServiceName("/backup/sessions/2-user_example.com")
	b := ccfile.KeychainServiceName("/backup/sessions/2-user_example.com")
	if a != b {
		t.Errorf("KeychainServiceName not deterministic: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "Claude Code-credentials-") {
		t.Errorf("KeychainServiceName() = %q, want prefix %q", a, "Claude Code-credentials-")
	}
	if len(a) != len("Claude Code-credentials-")+8 {
		t.Errorf("KeychainServiceName() = %q, want an 8 hex char digest suffix", a)
	}
}

func TestKeychainServiceName_NFCAndNFDComposedEqual(t *testing.T) {
	// "e-acute" as one precomposed rune (NFC, U+00E9) vs "e" + a combining
	// acute accent (NFD, U+0065 U+0301) render identically but compare
	// unequal as raw byte strings; keychain_service_name must NFC-normalize
	// first so both forms hash to the same service name. Built via explicit
	// escapes (not by typing the glyph twice) since a source editor would
	// normalize both occurrences to the same form and silently defeat the test.
	nfcPath := "/backup/sessions/2-caf\u00e9"  // precomposed e-acute
	nfdPath := "/backup/sessions/2-cafe\u0301" // e + combining acute accent

	if nfcPath == nfdPath {
		t.Fatal("test fixture invariant broken: raw strings must differ")
	}
	if norm.NFC.String(nfcPath) != norm.NFC.String(nfdPath) {
		t.Fatal("test fixture invariant broken: NFC forms must be equal")
	}

	a := ccfile.KeychainServiceName(nfcPath)
	b := ccfile.KeychainServiceName(nfdPath)
	if a != b {
		t.Errorf("KeychainServiceName(NFC) = %q != ccfile.KeychainServiceName(NFD) = %q", a, b)
	}
}

func TestKeychainServiceName_UnresolvedRawString(t *testing.T) {
	// Hashing must use the raw string as given -- never resolve/clean it --
	// so two differently-spelled (even if filesystem-equivalent) paths must
	// hash differently.
	a := ccfile.KeychainServiceName("/backup/sessions/2-user_example.com")
	b := ccfile.KeychainServiceName("/backup/sessions/./2-user_example.com")
	if a == b {
		t.Errorf("KeychainServiceName must not normalize/clean the path before hashing")
	}
}

// --- ReadProfileIdentity ---

func TestReadProfileIdentity_MissingFileReturnsNotOK(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := ccfile.ReadProfileIdentity(dir); ok {
		t.Error("expected ok=false for missing .claude.json")
	}
}

func TestReadProfileIdentity_CorruptJSONReturnsNotOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := ccfile.ReadProfileIdentity(dir); ok {
		t.Error("expected ok=false for corrupt .claude.json")
	}
}

func TestReadProfileIdentity_ReadsEmailAndOrg(t *testing.T) {
	dir := t.TempDir()
	body := `{"oauthAccount": {"emailAddress": "a@x.com", "organizationUuid": "org-1"}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	email, org, ok := ccfile.ReadProfileIdentity(dir)
	if !ok || email != "a@x.com" || org != "org-1" {
		t.Errorf("ReadProfileIdentity = (%q, %q, %v), want (a@x.com, org-1, true)", email, org, ok)
	}
}

func TestReadProfileIdentity_MissingEmailReturnsNotOK(t *testing.T) {
	dir := t.TempDir()
	body := `{"oauthAccount": {"organizationUuid": "org-1"}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := ccfile.ReadProfileIdentity(dir); ok {
		t.Error("expected ok=false when emailAddress is absent")
	}
}
