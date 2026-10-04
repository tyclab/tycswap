// export and import of an API-key account's base URL (DESIGN A46): the
// envelope carries it as baseUrl, the import checks it like add-token does
// and writes it into the record, and the key of such an account is the
// endpoint's, whatever its shape.
package transfer

import (
	"encoding/json"
	"strings"
	"testing"
)

const gwURL = "https://gw.example.com/anthropic"

func endpointAccount(number int, email, key string) map[string]any {
	a := oauthAccount(number, email, "")
	a["kind"] = "api_key"
	a["credentials"] = key
	a["baseUrl"] = gwURL
	return a
}

// TestExportCarriesTheBaseURL: the endpoint account is exported from its
// backup with its key and baseUrl, also while it is the live identity (its
// key is in no credential store then); other rows carry no baseUrl.
func TestExportCarriesTheBaseURL(t *testing.T) {
	f := newFakeAccounts(t)
	f.seedAccount("1", "alice@example.com", "", recordOpts{creds: oauthCreds, config: bloatConfig})
	f.seedAccount("2", "gw@example.com", "", recordOpts{kind: "api_key", creds: "sk-gw-0123456789", config: bloatConfig, baseURL: gwURL})
	f.curEmail, f.curOrg, f.curOK = "gw@example.com", "", true
	f.activeCreds = "" // nothing in Claude Code's store while the endpoint account is live

	var err error
	stdout, _ := captureIO(t, func() { err = Export(f, "-", "", false) })
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	accts := parseExport(t, strings.TrimRight(stdout, "\n"))["accounts"].([]any)
	if len(accts) != 2 {
		t.Fatalf("accounts = %v", accts)
	}
	gw := accts[1].(map[string]any)
	if gw["baseUrl"] != gwURL || gw["kind"] != "api_key" || gw["credentials"] != "sk-gw-0123456789" {
		t.Errorf("endpoint entry = %v", gw)
	}
	if _, present := accts[0].(map[string]any)["baseUrl"]; present {
		t.Errorf("OAuth entry carries a baseUrl: %v", accts[0])
	}
}

// TestImportStoresTheBaseURL: an endpoint account round-trips into the
// record (after kind and alias) with its gateway-shaped key.
func TestImportStoresTheBaseURL(t *testing.T) {
	f := newFakeAccounts(t)
	a := endpointAccount(2, "gw@example.com", "  sk-gw-0123456789 ")
	a["alias"] = "gw"
	if _, err := importText(t, f, envelopeJSON(nil, a), false); err != nil {
		t.Fatalf("Import: %v", err)
	}
	rec := f.record(t, "2")
	if rec["baseUrl"] != gwURL || rec["kind"] != "api_key" {
		t.Errorf("record = %v", rec)
	}
	raw := string(f.seq.Accounts["2"])
	if !strings.HasSuffix(raw, `"kind":"api_key","alias":"gw","baseUrl":"`+gwURL+`"}`) {
		t.Errorf("record bytes = %s, want baseUrl after kind and alias", raw)
	}
	if f.credsBackup["2"] != "sk-gw-0123456789" {
		t.Errorf("key = %q", f.credsBackup["2"])
	}
}

// TestImportRefusesABadBaseURL: the URL is checked like add-token's, it
// needs a key, and the key must be one header-safe string; nothing is written
// when any account fails.
func TestImportRefusesABadBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(a map[string]any)
		want string
	}{
		{"query", func(a map[string]any) { a["baseUrl"] = "https://gw.example.com/?k=v" }, "invalid baseUrl for gw@example.com"},
		{"user info", func(a map[string]any) { a["baseUrl"] = "https://u:p@gw.example.com" }, "invalid baseUrl for gw@example.com"},
		{"scheme", func(a map[string]any) { a["baseUrl"] = "file:///etc/passwd" }, "invalid baseUrl for gw@example.com"},
		{"not a string", func(a map[string]any) { a["baseUrl"] = 7 }, "baseUrl for gw@example.com must be a string"},
		{"oauth credentials", func(a map[string]any) {
			delete(a, "kind")
			a["credentials"] = map[string]any{"claudeAiOauth": map[string]any{"accessToken": "x"}}
		}, "baseUrl for gw@example.com needs API-key credentials"},
		{"key with a space", func(a map[string]any) { a["credentials"] = "two words" }, "must be the endpoint's key as one string"},
		{"key as an object", func(a map[string]any) { a["credentials"] = map[string]any{"k": "v"} }, "must be the endpoint's key as one string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAccounts(t)
			ok := oauthAccount(1, "first@example.com", "")
			a := endpointAccount(2, "gw@example.com", "sk-gw-0123456789")
			tc.edit(a)
			_, err := importText(t, f, envelopeJSON(nil, ok, a), false)
			if msg := transferErr(t, err); !strings.Contains(msg, tc.want) {
				t.Errorf("message = %q, want %q", msg, tc.want)
			}
			if len(f.writtenCreds) != 0 || f.writeSeqCount != 0 {
				t.Errorf("a refused import wrote: creds %v, sequence writes %d", f.writtenCreds, f.writeSeqCount)
			}
		})
	}
}

// TestExportImportRoundTripKeepsTheBaseURL: what export writes, import
// reads back into the same record.
func TestExportImportRoundTripKeepsTheBaseURL(t *testing.T) {
	src := newFakeAccounts(t)
	src.seedAccount("4", "gw@example.com", "", recordOpts{kind: "api_key", creds: "sk-gw-x", config: bloatConfig, baseURL: gwURL})
	var err error
	stdout, _ := captureIO(t, func() { err = Export(src, "-", "", false) })
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	dst := newFakeAccounts(t)
	if _, err := importText(t, dst, stdout, false); err != nil {
		t.Fatalf("Import: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(dst.seq.Accounts["4"], &rec); err != nil {
		t.Fatal(err)
	}
	if rec["baseUrl"] != gwURL || dst.credsBackup["4"] != "sk-gw-x" {
		t.Errorf("round trip: record %v, key %q", rec, dst.credsBackup["4"])
	}
}
