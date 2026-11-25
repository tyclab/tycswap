package ccfile_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
)

// referenceIdentity is ReadOAuthIdentityFrom as it was before DESIGN A116:
// the whole file decoded into map[string]any.
func referenceIdentity(path string) (string, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil || m == nil {
		return "", "", false
	}
	oauth, _ := m["oauthAccount"].(map[string]any)
	email, _ := oauth["emailAddress"].(string)
	if email == "" {
		return "", "", false
	}
	org, _ := oauth["organizationUuid"].(string)
	return email, org, true
}

// The identity read accepts and rejects exactly what the whole-file decode
// did, on every odd input.
func TestReadOAuthIdentityKeepsItsTolerance(t *testing.T) {
	const ok = `"oauthAccount":{"emailAddress":"a@example.com","organizationUuid":"org-1"}`
	padded := `{"numStartups":3,` + ok + `,"projects":{` + strings.Repeat(`"/p/x":{"history":[{"display":"go test ./... \"1e400\" \\","n":-12.5e-3}],"lastCost":0.25},`, 3500) + `"/p/y":{}}}`
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	for _, tc := range []struct {
		name, body string
		email, org string
		ok         bool
	}{
		{name: "missing file", body: "\x00missing"},
		{name: "empty file", body: ""},
		{name: "whitespace", body: " \n\t"},
		{name: "not JSON", body: "not json"},
		{name: "truncated", body: `{"oauthAccount":{"emailAddress":"a@example.com"`},
		{name: "null", body: "null"},
		{name: "array", body: `[{` + ok + `}]`},
		{name: "string", body: `"a@example.com"`},
		{name: "number", body: "5"},
		{name: "bool", body: "true"},
		{name: "empty object", body: "{}"},
		{name: "oauthAccount missing", body: `{"projects":{}}`},
		{name: "oauthAccount null", body: `{"oauthAccount":null}`},
		{name: "oauthAccount string", body: `{"oauthAccount":"a@example.com"}`},
		{name: "oauthAccount array", body: `{"oauthAccount":[{"emailAddress":"a@example.com"}]}`},
		{name: "oauthAccount number", body: `{"oauthAccount":1}`},
		{name: "email missing", body: `{"oauthAccount":{"organizationUuid":"org-1"}}`},
		{name: "email empty", body: `{"oauthAccount":{"emailAddress":""}}`},
		{name: "email number", body: `{"oauthAccount":{"emailAddress":7}}`},
		{name: "email null", body: `{"oauthAccount":{"emailAddress":null}}`},
		{name: "org number", body: `{"oauthAccount":{"emailAddress":"a@example.com","organizationUuid":7}}`, email: "a@example.com", ok: true},
		{name: "org null", body: `{"oauthAccount":{"emailAddress":"a@example.com","organizationUuid":null}}`, email: "a@example.com", ok: true},
		{name: "org missing", body: `{"oauthAccount":{"emailAddress":"a@example.com"}}`, email: "a@example.com", ok: true},
		{name: "valid", body: `{` + ok + `}`, email: "a@example.com", org: "org-1", ok: true},
		{name: "duplicate oauthAccount, last empty", body: `{` + ok + `,"oauthAccount":{}}`},
		{name: "duplicate oauthAccount, last valid", body: `{"oauthAccount":{},` + ok + `}`, email: "a@example.com", org: "org-1", ok: true},
		{name: "duplicate email", body: `{"oauthAccount":{"emailAddress":"x@example.com","emailAddress":"b@example.com"}}`, email: "b@example.com", ok: true},
		{name: "key case differs", body: `{"OAuthAccount":{"emailAddress":"a@example.com"}}`},
		{name: "inner key case differs", body: `{"oauthAccount":{"EmailAddress":"a@example.com"}}`},
		{name: "escaped keys", body: `{"oauthAccount":{"emailAddress":"a@example.com"}}`, email: "a@example.com", ok: true},
		{name: "trailing garbage", body: `{` + ok + `} x`},
		{name: "two objects", body: `{` + ok + `}{}`},
		{name: "trailing whitespace", body: `{` + ok + "}\n\n", email: "a@example.com", org: "org-1", ok: true},
		{name: "number too large elsewhere", body: `{` + ok + `,"big":1e400}`},
		{name: "number too large deep", body: `{` + ok + `,"p":[{"x":[-1e999]}]}`},
		{name: "number too large inside", body: `{"oauthAccount":{"emailAddress":"a@example.com","n":1E+309}}`},
		{name: "largest number", body: `{` + ok + `,"n":1.7976931348623157e308,"m":-0,"u":1e-400,"i":123456789012345678901234567890}`, email: "a@example.com", org: "org-1", ok: true},
		{name: "number-like strings", body: `{` + ok + `,"s":"1e400","t":"\"1e400\"","u":"\\","v":"-1e999"}`, email: "a@example.com", org: "org-1", ok: true},
		{name: "invalid UTF-8 elsewhere", body: `{` + ok + `,"x":"` + "\xff\xfe" + `"}`, email: "a@example.com", org: "org-1", ok: true},
		{name: "invalid UTF-8 in email", body: `{"oauthAccount":{"emailAddress":"a` + "\xff" + `@example.com"}}`, email: "a\uFFFD@example.com", ok: true},
		{name: "control character", body: `{"oauthAccount":{"emailAddress":"a` + "\x01" + `@example.com"}}`},
		{name: "byte order mark", body: "\xef\xbb\xbf{" + ok + "}"},
		{name: "nested too deep", body: `{` + ok + `,"deep":` + deep + `}`},
		{name: "300 KiB config", body: padded, email: "a@example.com", org: "org-1", ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".claude.json")
			if tc.body != "\x00missing" {
				if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			email, org, ok := ccfile.ReadOAuthIdentityFrom(path)
			if email != tc.email || org != tc.org || ok != tc.ok {
				t.Errorf("ReadOAuthIdentityFrom = %q, %q, %v; want %q, %q, %v", email, org, ok, tc.email, tc.org, tc.ok)
			}
			if re, ro, rok := referenceIdentity(path); email != re || org != ro || ok != rok {
				t.Errorf("ReadOAuthIdentityFrom = %q, %q, %v; the whole-file decode read %q, %q, %v", email, org, ok, re, ro, rok)
			}
			if pe, po, pok := ccfile.ReadProfileIdentity(dir); pe != email || po != org || pok != ok {
				t.Errorf("ReadProfileIdentity = %q, %q, %v; want ReadOAuthIdentityFrom's", pe, po, pok)
			}
		})
	}
	if len(padded) < 300*1024 {
		t.Fatalf("the large config is %d bytes, want at least 300 KiB", len(padded))
	}
}
