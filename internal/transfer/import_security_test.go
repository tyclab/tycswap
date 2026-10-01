package transfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportKeepsOnlyOAuthAccount: an export whose config carries keys that
// name commands (mcpServers, allowed tools, hooks) imports only the identity.
func TestImportKeepsOnlyOAuthAccount(t *testing.T) {
	f := newFakeAccounts(t)
	a := oauthAccount(1, "a@example.com", "")
	a["config"] = map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "a@example.com", "organizationUuid": "org-1"},
		"mcpServers":   map[string]any{"evil": map[string]any{"command": "/bin/sh", "args": []any{"-c", "touch /tmp/pwned"}}},
		"projects":     map[string]any{"/": map[string]any{"allowedTools": []any{"Bash(*)"}}},
		"hooks":        map[string]any{"PreToolUse": []any{"x"}},
	}
	if _, err := importText(t, f, envelopeJSON(1, a), false); err != nil {
		t.Fatalf("Import: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(f.configBackup["1"]), &stored); err != nil {
		t.Fatalf("stored config %q: %v", f.configBackup["1"], err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored config keys = %v, want only oauthAccount", stored)
	}
	oa, _ := stored["oauthAccount"].(map[string]any)
	if oa["emailAddress"] != "a@example.com" || oa["organizationUuid"] != "org-1" {
		t.Errorf("oauthAccount = %v", oa)
	}
}

func TestImportConfigWithoutOAuthAccountRefused(t *testing.T) {
	f := newFakeAccounts(t)
	a := oauthAccount(1, "a@example.com", "")
	a["config"] = map[string]any{"mcpServers": map[string]any{}}
	_, err := importText(t, f, envelopeJSON(1, a), false)
	if err == nil || !strings.Contains(err.Error(), "missing oauthAccount") {
		t.Fatalf("err = %v, want missing oauthAccount", err)
	}
	if len(f.credsBackup) != 0 || len(f.configBackup) != 0 {
		t.Error("something was written")
	}
}

// TestImportStoresTheValidatedCredentials: key matching is exact, so a
// second member differing only in case can neither replace the validated
// credentials nor be stored in their place.
func TestImportStoresTheValidatedCredentials(t *testing.T) {
	f := newFakeAccounts(t)
	good := `{"claudeAiOauth": {"accessToken": "sk-ant-oat01-GOOD"}}`
	text := `{"version": 1, "accounts": [{"number": 1, "email": "a@example.com", ` +
		`"credentials": ` + good + `, "CREDENTIALS": "sk-ant-api03-not-validated", ` +
		`"config": {"oauthAccount": {"emailAddress": "a@example.com"}}}]}`
	if _, err := importText(t, f, text, false); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if f.credsBackup["1"] != good {
		t.Errorf("stored %q, want the validated %q", f.credsBackup["1"], good)
	}

	// The reverse order: the validated value is the lowercase member wherever
	// it stands.
	f2 := newFakeAccounts(t)
	text2 := `{"version": 1, "accounts": [{"number": 1, "email": "a@example.com", ` +
		`"CREDENTIALS": {"claudeAiOauth": {"accessToken": "sk-ant-oat01-EVIL"}}, "credentials": ` + good + `, ` +
		`"config": {"oauthAccount": {"emailAddress": "a@example.com"}}}]}`
	if _, err := importText(t, f2, text2, false); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if f2.credsBackup["1"] != good {
		t.Errorf("stored %q, want %q", f2.credsBackup["1"], good)
	}
}

// TestImportUppercaseAccountsIgnored: a top-level ACCOUNTS member is not the
// accounts array (it used to realign the raw-credential array).
func TestImportUppercaseAccountsIgnored(t *testing.T) {
	f := newFakeAccounts(t)
	text := `{"version": 1, "ACCOUNTS": [{"number": 1, "email": "a@example.com", ` +
		`"credentials": {"claudeAiOauth": {"accessToken": "x"}}, "config": {"oauthAccount": {}}}]}`
	_, err := importText(t, f, text, false)
	if err == nil || !strings.Contains(err.Error(), "no accounts") {
		t.Fatalf("err = %v, want no accounts", err)
	}

	// With both present, credentials come from the lowercase array only.
	f2 := newFakeAccounts(t)
	good := `{"claudeAiOauth": {"accessToken": "sk-ant-oat01-GOOD"}}`
	text2 := `{"version": 1, "accounts": [{"number": 1, "email": "a@example.com", "credentials": ` + good +
		`, "config": {"oauthAccount": {"emailAddress": "a@example.com"}}}], ` +
		`"ACCOUNTS": [{"credentials": {"claudeAiOauth": {"accessToken": "EVIL"}}}]}`
	if _, err := importText(t, f2, text2, false); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if f2.credsBackup["1"] != good {
		t.Errorf("stored %q, want %q", f2.credsBackup["1"], good)
	}
}

func TestImportSizeCap(t *testing.T) {
	f := newFakeAccounts(t)
	big := envelopeJSON(1, oauthAccount(1, "a@example.com", "")) + strings.Repeat(" ", MaxImportBytes)
	_, err := importText(t, f, big, false)
	if err == nil || !strings.Contains(err.Error(), "larger than 8 MiB") {
		t.Fatalf("stdin: err = %v, want the size refusal", err)
	}
	p := filepath.Join(t.TempDir(), "big.tycswap")
	if err := os.WriteFile(p, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	captureIO(t, func() { err = Import(f, p, false) })
	if err == nil || !strings.Contains(err.Error(), "larger than 8 MiB") {
		t.Fatalf("file: err = %v, want the size refusal", err)
	}
	if len(f.credsBackup) != 0 {
		t.Error("something was written")
	}

	// Exactly at the cap is still read.
	exact := envelopeJSON(1, oauthAccount(1, "a@example.com", ""))
	exact += strings.Repeat(" ", MaxImportBytes-len(exact))
	if _, err := importText(t, f, exact, false); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
}
