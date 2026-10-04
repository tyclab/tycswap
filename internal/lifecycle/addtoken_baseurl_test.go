// add-token with a base URL (DESIGN A46): the key is an API key for that
// endpoint whatever its shape, and the record carries the URL.
package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccsettings"
)

const gwURL = "https://gw.example.com/anthropic"

// TestAddTokenBaseURLStoresTheEndpoint: a gateway-shaped key with a base URL is
// an API-key account; the key is stored verbatim (trimmed), the record carries
// kind and baseUrl after the usual fields, and the output names the host.
func TestAddTokenBaseURLStoresTheEndpoint(t *testing.T) {
	s := newStore(t)
	out := captureOut(t)
	if err := AddAccountFromTokenWithBaseURL(s, "  sk-gw-0123456789  ", "  "+gwURL+" ", nil, nil, false); err != nil {
		t.Fatalf("add-token --base-url: %v", err)
	}
	data := readSeq(t, s)
	r := rec(t, data, "1")
	if r.str("email") != "api-key-1@token.local" || r.str("kind") != "api_key" || r.str("baseUrl") != gwURL {
		t.Errorf("record = email %q kind %q baseUrl %q", r.str("email"), r.str("kind"), r.str("baseUrl"))
	}
	raw := data.Accounts["1"]
	if got, want := strings.Join(recKeys(t, &raw), ","), "email,uuid,organizationUuid,organizationName,added,kind,baseUrl"; got != want {
		t.Errorf("record keys = %s, want %s", got, want)
	}
	if creds, _ := s.ReadAccountCredentials("1", "api-key-1@token.local"); creds != "sk-gw-0123456789" {
		t.Errorf("stored key = %q", creds)
	}
	if got := s.AccountBaseURL("1"); got != gwURL {
		t.Errorf("AccountBaseURL = %q", got)
	}
	if !strings.Contains(out.String(), "(from API key for gw.example.com)") {
		t.Errorf("output does not name the endpoint host:\n%s", out.String())
	}
	if strings.Contains(out.String(), "sk-gw-") {
		t.Errorf("output echoes the key:\n%s", out.String())
	}
}

// TestAddTokenWithoutBaseURLIsUnchanged: no base URL, no baseUrl key, and a
// key that does not look like an Anthropic one is still a setup-token.
func TestAddTokenWithoutBaseURLIsUnchanged(t *testing.T) {
	s := newStore(t)
	if err := AddAccountFromTokenWithBaseURL(s, "sk-ant-api03-plain", "", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, readSeq(t, s), "1"); r.has("baseUrl") || r.str("kind") != "api_key" {
		t.Errorf("plain API key record: kind %q, baseUrl present %v", r.str("kind"), r.has("baseUrl"))
	}
}

// TestAddTokenBaseURLRefusals: an invalid URL is refused before the token is
// read (no prompt, nothing written); an invalid key for an endpoint is refused
// without writing.
func TestAddTokenBaseURLRefusals(t *testing.T) {
	for _, tc := range []struct{ name, token, url, want string }{
		{"query", "k", "https://gw.example.com/?k=v", "Invalid --base-url"},
		{"user info", "k", "https://u:p@gw.example.com", "Invalid --base-url"},
		{"scheme", "k", "gw.example.com", "Invalid --base-url"},
		{"key with a space", "two words", gwURL, "Invalid key for --base-url"},
		{"oauth blob as key", `{"claudeAiOauth":{}}`, gwURL, "Invalid key for --base-url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			p := &fakePrompter{secret: "should-not-be-read"}
			withPrompter(t, p)
			err := AddAccountFromTokenWithBaseURL(s, tc.token, tc.url, nil, nil, false)
			if errKind(err) != "ValidationError" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v (%s), want a ValidationError with %q", err, errKind(err), tc.want)
			}
			if data, _ := s.ReadSequence(); data != nil && len(data.Accounts) > 0 {
				t.Errorf("a refused add wrote %d account(s)", len(data.Accounts))
			}
		})
	}
	t.Run("url checked before the prompt", func(t *testing.T) {
		s := newStore(t)
		withPrompter(t, &fakePrompter{secret: "k"})
		out := captureOut(t)
		if err := AddAccountFromTokenWithBaseURL(s, "", "ftp://x.example", nil, nil, false); err == nil {
			t.Fatal("an ftp base URL was accepted")
		}
		if out.Len() != 0 {
			t.Errorf("output before the refusal: %q", out.String())
		}
	})
}

// TestAddTokenBaseURLRefreshSetsTheURLAsGiven: re-adding the same email
// refreshes the key in place and sets the URL exactly as given: a new URL
// replaces the old one, and a refresh without one removes it.
func TestAddTokenBaseURLRefreshSetsTheURLAsGiven(t *testing.T) {
	s := newStore(t)
	email := sp("gw@example.com")
	if err := AddAccountFromTokenWithBaseURL(s, "sk-ant-api03-one", gwURL, email, nil, false); err != nil {
		t.Fatal(err)
	}
	out := captureOut(t)
	if err := AddAccountFromTokenWithBaseURL(s, "sk-ant-api03-two", "https://other.example", email, nil, false); err != nil {
		t.Fatal(err)
	}
	data := readSeq(t, s)
	if len(data.Accounts) != 1 {
		t.Fatalf("refresh created a second record: %d", len(data.Accounts))
	}
	if got := rec(t, data, "1").str("baseUrl"); got != "https://other.example" {
		t.Errorf("baseUrl after refresh = %q", got)
	}
	if creds, _ := s.ReadAccountCredentials("1", "gw@example.com"); creds != "sk-ant-api03-two" {
		t.Errorf("key after refresh = %q", creds)
	}
	if !strings.Contains(out.String(), "(endpoint other.example)") {
		t.Errorf("refresh output: %q", out.String())
	}

	out.Reset()
	if err := AddAccountFromTokenWithBaseURL(s, "sk-ant-api03-three", "", email, nil, false); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, readSeq(t, s), "1"); r.has("baseUrl") || r.str("kind") != "api_key" {
		t.Errorf("refresh without a URL: baseUrl present %v, kind %q", r.has("baseUrl"), r.str("kind"))
	}
	if !strings.Contains(out.String(), "(endpoint removed)") {
		t.Errorf("refresh output: %q", out.String())
	}
}

// TestAddTokenBaseURLKeyThatIsNoAnthropicKeyNeedsTheURL: an endpoint account
// whose key is not shaped like an Anthropic key cannot be refreshed without
// its URL: without one the token reads as a setup-token, and the cross-kind
// guard refuses to put an OAuth token on the API-key slot.
func TestAddTokenBaseURLKeyThatIsNoAnthropicKeyNeedsTheURL(t *testing.T) {
	s := newStore(t)
	email := sp("gw@example.com")
	if err := AddAccountFromTokenWithBaseURL(s, "sk-gw-one", gwURL, email, nil, false); err != nil {
		t.Fatal(err)
	}
	err := AddAccountFromTokenWithBaseURL(s, "sk-gw-two", "", email, nil, false)
	if errKind(err) != "ValidationError" || !strings.Contains(err.Error(), "API-key account") {
		t.Fatalf("err = %v, want the cross-kind refusal", err)
	}
	if creds, _ := s.ReadAccountCredentials("1", "gw@example.com"); creds != "sk-gw-one" {
		t.Errorf("the refused refresh changed the key to %q", creds)
	}
	if got := s.AccountBaseURL("1"); got != gwURL {
		t.Errorf("the refused refresh changed the URL to %q", got)
	}
}

// TestAddTokenBaseURLIntoASlot: --slot works with a base URL as without one.
func TestAddTokenBaseURLIntoASlot(t *testing.T) {
	s := newStore(t)
	if err := AddAccountFromTokenWithBaseURL(s, "sk-gw-x", gwURL, nil, sp("4"), true); err != nil {
		t.Fatal(err)
	}
	r := rec(t, readSeq(t, s), "4")
	if r.str("email") != "api-key-4@token.local" || r.str("baseUrl") != gwURL {
		t.Errorf("slot 4 record: email %q baseUrl %q", r.str("email"), r.str("baseUrl"))
	}
}

// TestPurgeWarnsAboutAnEndpointRecord: with the record of an endpoint
// profile in the store, purge says before it asks that it deletes the way
// back; without one it says nothing of the kind.
func TestPurgeWarnsAboutAnEndpointRecord(t *testing.T) {
	for _, withRecord := range []bool{false, true} {
		s := newStore(t)
		seed(t, s, ip(1), switchable("1", "a@example.com"))
		if withRecord {
			if err := os.WriteFile(filepath.Join(s.BackupDir(), ccsettings.SidecarName), []byte(`{"version":1,"keys":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out := captureOut(t)
		withPrompter(t, &fakePrompter{prompts: []promptResp{{val: "n", ok: true}}})
		if err := Purge(s); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out.String(), "env.ANTHROPIC_BASE_URL"); got != withRecord {
			t.Errorf("record %v: warning shown %v:\n%s", withRecord, got, out.String())
		}
	}
}

// TestAddTokenRefreshOfTheLiveAccountSaysHowToActivateIt: refreshing the key
// (or URL) of the account the live login belongs to changes only its backup,
// and the output says that `switch <n> --force` makes it live.
func TestAddTokenRefreshOfTheLiveAccountSaysHowToActivateIt(t *testing.T) {
	s := newStore(t)
	email := sp("gw@example.com")
	if err := AddAccountFromTokenWithBaseURL(s, "sk-ant-api03-one", gwURL, email, nil, false); err != nil {
		t.Fatal(err)
	}
	out := captureOut(t)
	if err := AddAccountFromTokenWithBaseURL(s, "sk-ant-api03-two", gwURL, email, nil, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "--force") {
		t.Errorf("a refresh of an account that is not live gave the hint:\n%s", out.String())
	}
	cfg := `{"oauthAccount": {"emailAddress": "gw@example.com", "organizationUuid": null}}`
	if err := os.WriteFile(filepath.Join(s.Home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := AddAccountFromTokenWithBaseURL(s, "sk-ant-api03-three", "", email, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "tycswap switch 1 --force") {
		t.Errorf("refresh of the live account:\n%s", out.String())
	}
}
