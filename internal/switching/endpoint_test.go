// End-to-end switches onto and away from an API-key account with a base URL
// (DESIGN A46), on the file backend with a real temp store. Such an account
// keeps nothing in Claude Code's credential store: a switch onto it takes
// every login off that store (keeping the seat's MCP part) and writes the
// endpoint and the key into Claude Code's settings.json; a switch to any
// other account puts the two keys back exactly from the record, whatever else
// the user changed in the file meanwhile.
package switching

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
)

const (
	gwEmail  = "gw@x.com"
	gwURL    = "https://gw.example.com/anthropic"
	gwKey    = "sk-gw-0123456789"
	gw2Email = "gw2@x.com"
	gw2URL   = "https://two.example"
	gw2Key   = "sk-gw-two-9876543210"
)

// userSettings is Claude Code's settings.json as the user keeps it before any
// endpoint is written: a base URL and an Anthropic key of their own (a
// proxy), an env key beside them, hooks and permissions.
func userSettings() map[string]any {
	return map[string]any{
		"theme":       "dark",
		"permissions": map[string]any{"allow": []any{"Bash(ls)"}},
		"hooks":       map[string]any{"SessionStart": []any{}},
		"env":         map[string]any{"ANTHROPIC_BASE_URL": "https://user-proxy.example", "ANTHROPIC_API_KEY": "sk-ant-api03-users-own", "FOO": "bar"},
	}
}

func settingsPath(s *store.Store) string { return filepath.Join(s.Home, ".claude", "settings.json") }

func writeSettings(t *testing.T, s *store.Store, v map[string]any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath(s)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(s), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSettings(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(settingsPath(s))
	if err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("settings.json is not JSON: %v\n%s", err, raw)
	}
	return m
}

func settingsEnv(m map[string]any) map[string]any {
	e, _ := m["env"].(map[string]any)
	return e
}

func sidecarExists(s *store.Store) bool {
	_, err := os.Stat(ProfileSidecarPath(s))
	return err == nil
}

// endpointSeat seeds four slots — 1 a subscription login (live, with an MCP
// server login beside it), 2 a plain API key, 3 and 4 two endpoint accounts —
// and the user's own settings.json.
func endpointSeat(t *testing.T, s *store.Store) {
	t.Helper()
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2, 3, 4}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
		"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
		"3": record(map[string]any{"email": gwEmail, "organizationUuid": "", "kind": "api_key", "baseUrl": gwURL}),
		"4": record(map[string]any{"email": gw2Email, "organizationUuid": "", "kind": "api_key", "baseUrl": gw2URL}),
	}))
	seedBackup(t, s, "1", oauthSeatEmail, oauthCreds("acc-a", "ref-a"), "")
	seedBackup(t, s, "2", apiKeySeatEmail, apiKeySeatKey, "")
	seedBackup(t, s, "3", gwEmail, gwKey, "")
	seedBackup(t, s, "4", gw2Email, gw2Key, "")
	seedLive(t, s, oauthSeatEmail, "", withMCPOAuth(t, oauthCreds("acc-a", "ref-a"), "srv|1111", "mcp-live"))
	writeSettings(t, s, userSettings())
}

func switchTo(t *testing.T, s *store.Store, num string, force bool) {
	t.Helper()
	ApproveAPIKeySwitch(num) // the user confirmed (DESIGN A33); used up for a subscription slot
	if _, err := SwitchTo(s, num, true, force); err != nil {
		t.Fatalf("SwitchTo(%s, force=%v): %v", num, force, err)
	}
}

func activeNum(t *testing.T, s *store.Store) int {
	t.Helper()
	data, err := s.ReadSequence()
	if err != nil || data == nil || data.ActiveAccountNumber == nil {
		t.Fatalf("no active account: %v", err)
	}
	return *data.ActiveAccountNumber
}

// assertOnEndpoint checks the seat is on the endpoint account email with
// url/key: the two keys in settings.json beside every key of the user's but
// their ANTHROPIC_API_KEY (Claude Code would send it to the endpoint as
// X-Api-Key), no key and no login in Claude Code's credential store (the MCP
// login stays), the account's identity live, a record of the prior settings.
func assertOnEndpoint(t *testing.T, s *store.Store, email, url, key string, slot int) {
	t.Helper()
	got := readSettings(t, s)
	want := userSettings()
	settingsEnv(want)["ANTHROPIC_BASE_URL"] = url
	settingsEnv(want)["ANTHROPIC_AUTH_TOKEN"] = key
	delete(settingsEnv(want), "ANTHROPIC_API_KEY")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings.json on the endpoint:\n got %v\nwant %v", got, want)
	}
	if live := readActiveCreds(t, s); live != "" {
		t.Errorf("Claude Code's credential store holds %q, want no credential", live)
	}
	cfg := liveConfig(t, s)
	if k, present := cfg["primaryApiKey"]; present {
		t.Errorf("primaryApiKey = %v, want none for an endpoint account", k)
	}
	if approvedKey(cfg, key) {
		t.Error("the endpoint key was recorded as an approved managed key")
	}
	if got := oauthEmail(cfg); got != email {
		t.Errorf("oauthAccount email = %q, want %q", got, email)
	}
	if got := mcpTokenOf(t, string(mustRead(t, liveCredentialsPath(s))), "srv|1111"); got != "mcp-live" {
		t.Errorf("the seat's MCP server login = %q, want it kept", got)
	}
	if !sidecarExists(s) {
		t.Error("no record of the prior settings")
	}
	if got := activeNum(t, s); got != slot {
		t.Errorf("active account = %d, want %d", got, slot)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertOnSubscription checks the seat is back on slot 1's login with the
// user's settings exactly as before (plus wantExtra, edits made meanwhile).
func assertOnSubscription(t *testing.T, s *store.Store, wantSettings map[string]any) {
	t.Helper()
	if got := readSettings(t, s); !reflect.DeepEqual(got, wantSettings) {
		t.Errorf("settings.json after the switch away:\n got %v\nwant %v", got, wantSettings)
	}
	if sidecarExists(s) {
		t.Error("the record survived the revert")
	}
	live := readActiveCreds(t, s)
	if got := oauth.ExtractAccessToken(live); got != "acc-a" {
		t.Errorf("live access token = %q, want slot 1's", got)
	}
	if !hasMCPOAuth(t, live) {
		t.Errorf("live credential %s lost the seat's MCP server login", live)
	}
	if got := oauthEmail(liveConfig(t, s)); got != oauthSeatEmail {
		t.Errorf("oauthAccount email = %q", got)
	}
	if k, present := liveConfig(t, s)["primaryApiKey"]; present {
		t.Errorf("primaryApiKey = %v on a subscription login", k)
	}
	if got := activeNum(t, s); got != 1 {
		t.Errorf("active account = %d, want 1", got)
	}
}

// TestSwitchOntoAnEndpointFromASubscriptionAndBack: onto the endpoint account
// and back, with the user editing settings.json while on it. The edits
// survive, the user's own ANTHROPIC_BASE_URL comes back, the token goes, and
// both slots keep their credentials.
func TestSwitchOntoAnEndpointFromASubscriptionAndBack(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)

	switchTo(t, s, "3", false)
	assertOnEndpoint(t, s, gwEmail, gwURL, gwKey, 3)
	if got, _ := s.ReadAccountCredentials("1", oauthSeatEmail); oauth.ExtractAccessToken(got) != "acc-a" {
		t.Errorf("slot 1 backup = %q, want its login", got)
	}

	// The user changes other settings while on the endpoint.
	m := readSettings(t, s)
	m["model"] = "opus"
	settingsEnv(m)["MINE"] = "1"
	writeSettings(t, s, m)

	switchTo(t, s, "1", false)
	want := userSettings()
	want["model"] = "opus"
	settingsEnv(want)["MINE"] = "1"
	assertOnSubscription(t, s, want)
	if got, _ := s.ReadAccountCredentials("3", gwEmail); got != gwKey {
		t.Errorf("slot 3 = %q after the switch away, want its key untouched", got)
	}
	if prev, _ := s.Creds.ReadPrev("3", gwEmail); prev != "" {
		t.Errorf("slot 3 retained a .prev %q: its backup was displaced", prev)
	}
}

// TestSwitchFromOneEndpointToAnother: the second endpoint replaces the first
// in settings.json, and the record keeps the ORIGINAL pre-profile values, so
// the switch to the subscription account lands on the user's settings, not on
// the first endpoint.
func TestSwitchFromOneEndpointToAnother(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	switchTo(t, s, "3", false)
	switchTo(t, s, "4", false)
	assertOnEndpoint(t, s, gw2Email, gw2URL, gw2Key, 4)
	if got, _ := s.ReadAccountCredentials("3", gwEmail); got != gwKey {
		t.Errorf("slot 3 = %q, want its key (nothing from the store may be backed up over it)", got)
	}

	var rec struct {
		Keys map[string]struct {
			Present bool `json:"present"`
			Value   any  `json:"value"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(mustRead(t, ProfileSidecarPath(s)), &rec); err != nil {
		t.Fatal(err)
	}
	if b := rec.Keys[ccsettings.KeyBaseURL]; !b.Present || b.Value != "https://user-proxy.example" {
		t.Errorf("recorded base URL = %+v, want the user's own", b)
	}
	if tok := rec.Keys[ccsettings.KeyAuthToken]; tok.Present {
		t.Errorf("recorded token = %+v, want absent (it was before the first endpoint)", tok)
	}

	switchTo(t, s, "1", false)
	assertOnSubscription(t, s, userSettings())
}

// TestSwitchBetweenAPlainKeyAndAnEndpoint: from a plain API key (in
// primaryApiKey with its approval) onto an endpoint the key leaves the
// credential store and its slot keeps it; back onto the plain key the
// settings are restored and the key is live again.
func TestSwitchBetweenAPlainKeyAndAnEndpoint(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	switchTo(t, s, "2", false)
	if got := readActiveCreds(t, s); got != apiKeySeatKey {
		t.Fatalf("precondition: live = %q, want the plain key", got)
	}
	if !reflect.DeepEqual(readSettings(t, s), userSettings()) {
		t.Fatal("a switch onto a key without a URL touched settings.json")
	}
	if sidecarExists(s) {
		t.Fatal("a switch onto a key without a URL wrote a record")
	}

	switchTo(t, s, "3", false)
	assertOnEndpoint(t, s, gwEmail, gwURL, gwKey, 3)
	if got, _ := s.ReadAccountCredentials("2", apiKeySeatEmail); got != apiKeySeatKey {
		t.Errorf("slot 2 backup = %q, want its key", got)
	}

	switchTo(t, s, "2", false)
	if got := readSettings(t, s); !reflect.DeepEqual(got, userSettings()) {
		t.Errorf("settings.json = %v, want the user's", got)
	}
	if sidecarExists(s) {
		t.Error("record survived")
	}
	if got := readActiveCreds(t, s); got != apiKeySeatKey {
		t.Errorf("live = %q, want the plain key back", got)
	}
	if got := liveConfig(t, s)["primaryApiKey"]; got != apiKeySeatKey {
		t.Errorf("primaryApiKey = %v, want the plain key", got)
	}
}

// TestForcedSwitchesHonourTheEndpoint: --force onto the endpoint, --force
// onto the endpoint already active after its key was re-added (the new key
// goes in, the record keeps the original priors), and --force back.
func TestForcedSwitchesHonourTheEndpoint(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	switchTo(t, s, "3", true)
	assertOnEndpoint(t, s, gwEmail, gwURL, gwKey, 3)

	const rotated = "sk-gw-rotated-555"
	if err := s.WriteAccountCredentials("3", gwEmail, rotated); err != nil {
		t.Fatal(err)
	}
	switchTo(t, s, "3", true)
	assertOnEndpoint(t, s, gwEmail, gwURL, rotated, 3)

	switchTo(t, s, "1", true)
	assertOnSubscription(t, s, userSettings())
}

// TestFreshMachineOntoAnEndpoint: with no live login at all, the activation
// writes the identity and the endpoint, and the switch away restores the
// settings.
func TestFreshMachineOntoAnEndpoint(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	for _, p := range []string{filepath.Join(s.Home, ".claude.json"), liveCredentialsPath(s)} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	switchTo(t, s, "3", false)
	got := readSettings(t, s)
	if e := settingsEnv(got); e["ANTHROPIC_BASE_URL"] != gwURL || e["ANTHROPIC_AUTH_TOKEN"] != gwKey {
		t.Errorf("settings.json env = %v, want the endpoint", e)
	}
	if live := readActiveCreds(t, s); live != "" {
		t.Errorf("credential store = %q, want none", live)
	}
	if got := oauthEmail(liveConfig(t, s)); got != gwEmail {
		t.Errorf("identity = %q", got)
	}
	if _, err := os.Stat(liveCredentialsPath(s)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a credentials file appeared: %v", err)
	}

	switchTo(t, s, "1", false)
	if got := readSettings(t, s); !reflect.DeepEqual(got, userSettings()) {
		t.Errorf("settings.json = %v, want the user's", got)
	}
	if got := oauth.ExtractAccessToken(readActiveCreds(t, s)); got != "acc-a" {
		t.Errorf("live token = %q", got)
	}
}

// TestRotationFromAnEndpointRevertsIt: the bare rotation from an endpoint
// account skips the other API-key accounts and lands on the subscription
// account, putting the settings back.
func TestRotationFromAnEndpointRevertsIt(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	switchTo(t, s, "3", false)
	if _, err := Switch(s, nil, true, nil, nil); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	assertOnSubscription(t, s, userSettings())
}

// configBreaksOnManagedDelete is a Keychain on which the managed-key item's
// delete — the last thing every credential write of these switches does
// (clearing the managed key) — leaves ~/.claude.json unparseable once armed,
// so the switch fails at its ~/.claude.json step, after the credential store
// and settings.json were written.
type configBreaksOnManagedDelete struct {
	*keychain.Fake
	t      *testing.T
	config string
	armed  *bool
}

func (k configBreaksOnManagedDelete) Delete(service, account string) error {
	err := k.Fake.Delete(service, account)
	if *k.armed && service == managedKeychainService {
		if werr := os.WriteFile(k.config, []byte("{"), 0o600); werr != nil {
			k.t.Fatal(werr)
		}
	}
	return err
}

// seatFiles is every file of the seat a switch writes except ~/.claude.json
// (which the failure itself breaks), as bytes ("<absent>" for none).
func seatFiles(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range []string{settingsPath(s), ProfileSidecarPath(s), liveCredentialsPath(s)} {
		b, err := os.ReadFile(p)
		switch {
		case errors.Is(err, os.ErrNotExist):
			out[p] = "<absent>"
		case err != nil:
			t.Fatal(err)
		default:
			out[p] = string(b)
		}
	}
	return out
}

// keychainItems is both Claude Code Keychain items, "<absent>" for none.
func keychainItems(kc keychain.KeychainClient) [2]string {
	var out [2]string
	for i, svc := range []string{oauthKeychainService, managedKeychainService} {
		v, ok, _ := kc.Get(svc, keychain.AccountName())
		if !ok {
			v = "<absent>"
		}
		out[i] = v
	}
	return out
}

// TestAFailedSwitchLeavesSettingsAsBefore: a switch whose ~/.claude.json
// update fails after the credential store and settings.json were written
// puts settings.json, the record, the credentials file and both Keychain
// items back as they were — onto an endpoint, away from one, between two,
// and from a plain key — on the normal path and with --force.
func TestAFailedSwitchLeavesSettingsAsBefore(t *testing.T) {
	for _, tc := range []struct {
		name   string
		from   string // "" stays on slot 1
		target string
		// bare: the seat has no MCP server login, so on an endpoint the
		// credential store holds nothing at all.
		bare bool
	}{
		{"onto an endpoint", "", "3", false},
		{"away from an endpoint", "3", "1", false},
		{"away from an endpoint with nothing else live", "3", "1", true},
		{"from an endpoint to another", "3", "4", false},
		{"from a plain key onto an endpoint", "2", "3", false},
	} {
		for _, force := range []bool{false, true} {
			name := tc.name
			if force {
				name += " with --force"
			}
			t.Run(name, func(t *testing.T) {
				s := newTestStore(t, nil)
				endpointSeat(t, s)
				login := withMCPOAuth(t, oauthCreds("acc-a", "ref-a"), "srv|1111", "mcp-live")
				if tc.bare {
					login = oauthCreds("acc-a", "ref-a")
					if err := os.WriteFile(liveCredentialsPath(s), []byte(login), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				armed := false
				kc := configBreaksOnManagedDelete{Fake: keychain.NewFake(), t: t, config: filepath.Join(s.Home, ".claude.json"), armed: &armed}
				kc.Seed(oauthKeychainService, keychain.AccountName(), login)
				s.Creds = credstore.New(credstore.Config{Platform: platform.MacOS, CredentialsDir: s.CredentialsDir}, kc, s.Clk, s.Log)
				if tc.from != "" {
					switchTo(t, s, tc.from, false)
				}
				before := seatFiles(t, s)
				itemsBefore := keychainItems(kc)
				activeBefore := activeNum(t, s)
				liveBefore := readActiveCreds(t, s)

				armed = true
				ApproveAPIKeySwitch(tc.target)
				_, err := SwitchTo(s, tc.target, true, force)
				if err == nil {
					t.Fatal("the switch succeeded although ~/.claude.json could not be updated")
				}
				if strings.Contains(err.Error(), "Confirm the switch") {
					t.Fatalf("refused for want of an approval: %v", err)
				}
				if !strings.Contains(err.Error(), "Claude config") {
					t.Fatalf("err = %v, want the ~/.claude.json failure", err)
				}
				after := seatFiles(t, s)
				for p, b := range before {
					if after[p] != b {
						t.Errorf("%s changed by the failed switch:\n before %s\n after  %s", filepath.Base(p), b, after[p])
					}
				}
				if got := keychainItems(kc); got != itemsBefore {
					t.Errorf("Keychain items = %q, want %q", got, itemsBefore)
				}
				if got := activeNum(t, s); got != activeBefore {
					t.Errorf("active account = %d, want %d", got, activeBefore)
				}
				if got := readActiveCreds(t, s); got != liveBefore {
					t.Errorf("live credential = %q, want %q", got, liveBefore)
				}
			})
		}
	}
}

// TestASwitchStopsAtACorruptRecordOrSettingsFile: a corrupt record or an
// unparseable settings.json stops a switch that would write them before the
// live login is touched. A subscription-to-subscription switch with no record
// does not read settings.json for anything that could stop it.
func TestASwitchStopsAtACorruptRecordOrSettingsFile(t *testing.T) {
	t.Run("corrupt record", func(t *testing.T) {
		s := newTestStore(t, nil)
		endpointSeat(t, s)
		switchTo(t, s, "3", false)
		if err := os.WriteFile(ProfileSidecarPath(s), []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := seatFiles(t, s)
		backupBefore, _ := s.ReadAccountConfig("3", gwEmail)
		if err := os.WriteFile(filepath.Join(s.Home, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"`+gwEmail+`","organizationUuid":""},"changed":"since the switch"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := SwitchTo(s, "1", true, false)
		if err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("err = %v, want the corrupt-record refusal", err)
		}
		// Refused before step 1: not even the outgoing backup was written.
		if got, _ := s.ReadAccountConfig("3", gwEmail); got != backupBefore {
			t.Errorf("the outgoing backup was rewritten by a refused switch: %s", got)
		}
		for p, b := range before {
			if got := seatFiles(t, s)[p]; got != b {
				t.Errorf("%s changed: %s", filepath.Base(p), got)
			}
		}
		if got := activeNum(t, s); got != 3 {
			t.Errorf("active = %d, want 3", got)
		}
	})
	t.Run("unparseable settings", func(t *testing.T) {
		s := newTestStore(t, nil)
		endpointSeat(t, s)
		if err := os.WriteFile(settingsPath(s), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := seatFiles(t, s)
		ApproveAPIKeySwitch("3")
		_, err := SwitchTo(s, "3", true, false)
		if err == nil || !strings.Contains(err.Error(), "refusing to rewrite") {
			t.Fatalf("err = %v, want the settings refusal", err)
		}
		for p, b := range before {
			if got := seatFiles(t, s)[p]; got != b {
				t.Errorf("%s changed: %s", filepath.Base(p), got)
			}
		}
		if got := activeNum(t, s); got != 1 {
			t.Errorf("active = %d, want 1", got)
		}
	})
	t.Run("symlinked settings", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on Windows")
		}
		s := newTestStore(t, nil)
		endpointSeat(t, s)
		target := filepath.Join(t.TempDir(), "dotfiles-settings.json")
		if err := os.Rename(settingsPath(s), target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, settingsPath(s)); err != nil {
			t.Fatal(err)
		}
		before := seatFiles(t, s)
		ApproveAPIKeySwitch("3")
		_, err := SwitchTo(s, "3", true, false)
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("err = %v, want the symlink refusal", err)
		}
		for p, b := range before {
			if got := seatFiles(t, s)[p]; got != b {
				t.Errorf("%s changed: %s", filepath.Base(p), got)
			}
		}
		if link, err := os.Readlink(settingsPath(s)); err != nil || link != target {
			t.Errorf("settings.json is no longer the link: %q, %v", link, err)
		}
	})
	t.Run("subscription switch ignores an unparseable settings file", func(t *testing.T) {
		s := newTestStore(t, nil)
		endpointSeat(t, s)
		writeSeq(t, s, seqData(ptrInt(1), []int{1, 5}, map[string]json.RawMessage{
			"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
			"5": record(map[string]any{"email": "b@x.com", "organizationUuid": ""}),
		}))
		seedBackup(t, s, "5", "b@x.com", oauthCreds("acc-b", "ref-b"), "")
		if err := os.WriteFile(settingsPath(s), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		switchTo(t, s, "5", false)
		if got := string(mustRead(t, settingsPath(s))); got != "{not json" {
			t.Errorf("settings.json = %q", got)
		}
	})
}

// TestALostRecordStillComesOut: with the record removed by hand, the switch
// away still takes the endpoint out, because settings.json holds exactly an
// endpoint tycswap knows and its key. What it replaced cannot come back.
func TestALostRecordStillComesOut(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	writeSettings(t, s, map[string]any{"theme": "dark"})
	switchTo(t, s, "3", false)
	if err := os.Remove(ProfileSidecarPath(s)); err != nil {
		t.Fatal(err)
	}
	switchTo(t, s, "1", false)
	if got := readSettings(t, s); !reflect.DeepEqual(got, map[string]any{"theme": "dark"}) {
		t.Errorf("settings.json = %v, want the endpoint gone", got)
	}
}

// TestASwitchAwayLeavesAForeignEndpoint: without a record, an endpoint and
// token in settings.json that are not one of tycswap's accounts are the
// user's, and a switch leaves them.
func TestASwitchAwayLeavesAForeignEndpoint(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	foreign := map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": gwURL, "ANTHROPIC_AUTH_TOKEN": "not-ours"}}
	writeSettings(t, s, foreign)
	before := mustRead(t, settingsPath(s))
	switchTo(t, s, "2", false)
	if after := mustRead(t, settingsPath(s)); !bytes.Equal(before, after) {
		t.Errorf("a foreign endpoint was touched: %s", after)
	}
}

// TestAnEndpointAccountThatCannotBeWrittenIsRefused: an invalid stored URL,
// a stored key that cannot be a header, and an API-key account without a URL
// whose key is not an Anthropic key all stop the switch with nothing written.
func TestAnEndpointAccountThatCannotBeWrittenIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record map[string]any
		key    string
		want   string
	}{
		{"invalid url", map[string]any{"kind": "api_key", "baseUrl": "https://u:p@gw.example.com"}, gwKey, "invalid base URL"},
		{"key with a space", map[string]any{"kind": "api_key", "baseUrl": gwURL}, "two words", "cannot be written"},
		{"gateway key without a url", map[string]any{"kind": "api_key"}, gwKey, "not an Anthropic API key"},
	} {
		for _, force := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				s := newTestStore(t, nil)
				endpointSeat(t, s)
				rec := map[string]any{"email": gwEmail, "organizationUuid": ""}
				for k, v := range tc.record {
					rec[k] = v
				}
				data, _ := s.ReadSequence()
				data.Accounts["3"] = record(rec)
				writeSeq(t, s, data)
				if err := s.WriteAccountCredentials("3", gwEmail, tc.key); err != nil {
					t.Fatal(err)
				}
				before := seatFiles(t, s)
				ApproveAPIKeySwitch("3")
				_, err := SwitchTo(s, "3", true, force)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want %q", err, tc.want)
				}
				for p, b := range before {
					if got := seatFiles(t, s)[p]; got != b {
						t.Errorf("%s changed: %s", filepath.Base(p), got)
					}
				}
			})
		}
	}
}

// TestTheApprovalNamesTheEndpoint: without an approval the refusal says where
// the requests would go, and the follow-up of a switch that rewrote
// settings.json says to restart, in both directions.
func TestTheApprovalNamesTheEndpoint(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	_, err := SwitchTo(s, "3", true, false)
	if err == nil || err.Error() != ErrEndpointNeedsApproval("3", "gw.example.com").Error() {
		t.Fatalf("err = %v, want the endpoint approval refusal", err)
	}
	ApproveAPIKeySwitch("3")
	out := captureStdout(t, func() {
		if _, err := SwitchTo(s, "3", false, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, EndpointAppliedNote("gw.example.com")) || strings.Contains(out, APIKeyRestartNote) {
		t.Errorf("follow-up onto the endpoint:\n%s", out)
	}
	out = captureStdout(t, func() {
		if _, err := SwitchTo(s, "1", false, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, EndpointRevertedNote) || strings.Contains(out, "no restart needed") {
		t.Errorf("follow-up away from the endpoint:\n%s", out)
	}
}

// TestSwitchOntoAnEndpoint_macOS: the Keychain variant. The subscription
// login lives in the OAuth Keychain item with its shadow file, a plain key
// in the managed item. Onto the endpoint both items lose their login and
// nothing is stored in either; back on the subscription account the login is
// in the item again.
func TestSwitchOntoAnEndpoint_macOS(t *testing.T) {
	for _, from := range []string{"1", "2"} {
		t.Run("from slot "+from, func(t *testing.T) {
			s := newTestStore(t, nil)
			endpointSeat(t, s)
			login := oauthCreds("acc-a", "ref-a")
			kc := keychain.NewFake()
			kc.Seed(oauthKeychainService, keychain.AccountName(), login)
			if err := os.WriteFile(liveCredentialsPath(s), []byte(login), 0o600); err != nil {
				t.Fatal(err)
			}
			s.Creds = credstore.New(credstore.Config{Platform: platform.MacOS, CredentialsDir: s.CredentialsDir}, kc, s.Clk, s.Log)
			if from == "2" {
				switchTo(t, s, "2", false)
				if item, ok, _ := kc.Get(managedKeychainService, keychain.AccountName()); !ok || item != apiKeySeatKey {
					t.Fatalf("precondition: managed item = %q, %v", item, ok)
				}
			}

			switchTo(t, s, "3", false)
			if live := readActiveCreds(t, s); live != "" {
				t.Errorf("live credential = %q, want none", live)
			}
			if item, ok, _ := kc.Get(managedKeychainService, keychain.AccountName()); ok {
				t.Errorf("managed Keychain item = %q, want none for an endpoint account", item)
			}
			if item, ok, _ := kc.Get(oauthKeychainService, keychain.AccountName()); ok && oauth.ExtractAccessToken(item) != "" {
				t.Errorf("OAuth Keychain item still holds a login: %q", item)
			}
			if e := settingsEnv(readSettings(t, s)); e["ANTHROPIC_AUTH_TOKEN"] != gwKey || e["ANTHROPIC_BASE_URL"] != gwURL {
				t.Errorf("settings.json env = %v", e)
			}

			switchTo(t, s, "1", false)
			if got := readSettings(t, s); !reflect.DeepEqual(got, userSettings()) {
				t.Errorf("settings.json = %v, want the user's", got)
			}
			item, ok, _ := kc.Get(oauthKeychainService, keychain.AccountName())
			if !ok || oauth.ExtractAccessToken(item) != "acc-a" {
				t.Errorf("OAuth Keychain item = %q, %v; want slot 1's login", item, ok)
			}
		})
	}
}

// TestALostRecordIsNotTakenForTheUsersSettings: with the record removed by
// hand while an endpoint is in place, a switch onto another endpoint does not
// record the first one as what the user had, so the way back removes both
// instead of putting the first endpoint back. --force onto the same account
// re-applies it the same way.
func TestALostRecordIsNotTakenForTheUsersSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		then string
	}{
		{"onto another endpoint", "4"},
		{"--force onto the same one", "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t, nil)
			endpointSeat(t, s)
			writeSettings(t, s, map[string]any{"theme": "dark"})
			switchTo(t, s, "3", false)
			if err := os.Remove(ProfileSidecarPath(s)); err != nil {
				t.Fatal(err)
			}
			switchTo(t, s, tc.then, tc.then == "3")
			switchTo(t, s, "1", false)
			if got := readSettings(t, s); !reflect.DeepEqual(got, map[string]any{"theme": "dark"}) {
				t.Errorf("settings.json = %v, want no endpoint left", got)
			}
		})
	}
}

// managedDeleteFails is a Keychain whose managed-key item cannot be deleted.
type managedDeleteFails struct{ *keychain.Fake }

func (k managedDeleteFails) Delete(service, account string) error {
	if service == managedKeychainService {
		return &keychain.KeychainError{Msg: "delete refused"}
	}
	return k.Fake.Delete(service, account)
}

// TestASwitchOntoAnEndpointStopsWhenAKeyStaysLive_macOS: when a managed
// Keychain item cannot be removed, Claude Code would send that key along to
// the endpoint, so the switch fails and rolls back: from the plain-key
// account its key is live as before; from a subscription login beside a
// stale item, the login the clearing had already taken off is put back.
// settings.json and the record are untouched either way.
func TestASwitchOntoAnEndpointStopsWhenAKeyStaysLive_macOS(t *testing.T) {
	login := withMCPOAuth(t, oauthCreds("acc-a", "ref-a"), "srv|1111", "mcp-live")
	for _, tc := range []struct {
		name, from, stale, wantLive string
		active                      int
	}{
		{"from the plain-key account", "2", "", apiKeySeatKey, 2},
		{"from a login beside a stale key", "", "sk-ant-api03-stale-0000", login, 1},
	} {
		for _, force := range []bool{false, true} {
			name := tc.name
			if force {
				name += " with --force"
			}
			t.Run(name, func(t *testing.T) {
				s := newTestStore(t, nil)
				endpointSeat(t, s)
				kc := managedDeleteFails{keychain.NewFake()}
				kc.Seed(oauthKeychainService, keychain.AccountName(), login)
				if tc.stale != "" {
					kc.Seed(managedKeychainService, keychain.AccountName(), tc.stale)
				}
				s.Creds = credstore.New(credstore.Config{Platform: platform.MacOS, CredentialsDir: s.CredentialsDir}, kc, s.Clk, s.Log)
				if tc.from != "" {
					switchTo(t, s, tc.from, false)
				}
				before := seatFiles(t, s)
				itemsBefore := keychainItems(kc)

				ApproveAPIKeySwitch("3")
				if _, err := SwitchTo(s, "3", true, force); err == nil || !strings.Contains(err.Error(), "still readable") {
					t.Fatalf("err = %v, want the switch to stop at the key left live", err)
				}
				if got := readActiveCreds(t, s); got != tc.wantLive {
					t.Errorf("live credential = %q, want %q", got, tc.wantLive)
				}
				if got := keychainItems(kc); got != itemsBefore {
					t.Errorf("Keychain items = %q, want %q", got, itemsBefore)
				}
				for p, b := range before {
					if got := seatFiles(t, s)[p]; got != b {
						t.Errorf("%s changed: %s", filepath.Base(p), got)
					}
				}
				if got := activeNum(t, s); got != tc.active {
					t.Errorf("active = %d, want %d", got, tc.active)
				}
			})
		}
	}
}

// TestChangingTheActiveAccountsURL: a refresh that removes the URL of the
// active endpoint account, or adds one to the active plain-key account,
// leaves the live state as the last switch made it. The switch away still
// works from either: the profile is reverted from its record, and the plain
// key is the account's own, not a stranger's credential to preserve. The
// refresh says how to make the change live.
func TestChangingTheActiveAccountsURL(t *testing.T) {
	t.Run("URL removed from the active endpoint account", func(t *testing.T) {
		s := newTestStore(t, nil)
		endpointSeat(t, s)
		switchTo(t, s, "3", false)
		data, _ := s.ReadSequence()
		data.Accounts["3"] = record(map[string]any{"email": gwEmail, "organizationUuid": "", "kind": "api_key"})
		writeSeq(t, s, data)
		if err := s.WriteAccountCredentials("3", gwEmail, "sk-ant-api03-now-plain"); err != nil {
			t.Fatal(err)
		}
		switchTo(t, s, "1", false)
		assertOnSubscription(t, s, userSettings())
	})
	t.Run("URL added to the active plain-key account", func(t *testing.T) {
		s := newTestStore(t, nil)
		endpointSeat(t, s)
		switchTo(t, s, "2", false)
		data, _ := s.ReadSequence()
		data.Accounts["2"] = record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key", "baseUrl": gwURL})
		writeSeq(t, s, data)
		out := captureStdout(t, func() {
			ApproveAPIKeySwitch("1")
			if _, err := SwitchTo(s, "1", false, false); err != nil {
				t.Fatal(err)
			}
		})
		if strings.Contains(out, "does not match") || strings.Contains(out, "preserved") {
			t.Errorf("the account's own key was treated as a stranger's:\n%s", out)
		}
		if unclaimed, _ := s.Creds.ListUnclaimed(); len(unclaimed) != 0 {
			t.Errorf("the account's own key was stashed: %v", unclaimed)
		}
		assertOnSubscription(t, s, userSettings())
		if got, _ := s.ReadAccountCredentials("2", apiKeySeatEmail); got != apiKeySeatKey {
			t.Errorf("slot 2 = %q, want its key", got)
		}
	})
}

// oauthReadFails is a Keychain whose OAuth item cannot be read.
type oauthReadFails struct{ *keychain.Fake }

func (k oauthReadFails) Get(service, account string) (string, bool, error) {
	if service == oauthKeychainService {
		return "", false, &keychain.KeychainError{Msg: "read refused"}
	}
	return k.Fake.Get(service, account)
}

// TestASwitchAwayFromAnEndpointNeedsAReadableKeychain_macOS: on an endpoint
// account an empty credential store is normal, but one whose Keychain did
// not answer may hold the seat's MCP logins, which a rollback could not put
// back: the switch away is refused as for any unreadable credential.
func TestASwitchAwayFromAnEndpointNeedsAReadableKeychain_macOS(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	fake := keychain.NewFake()
	s.Creds = credstore.New(credstore.Config{Platform: platform.MacOS, CredentialsDir: s.CredentialsDir}, fake, s.Clk, s.Log)
	switchTo(t, s, "3", false)
	if err := os.Remove(liveCredentialsPath(s)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s.Creds = credstore.New(credstore.Config{Platform: platform.MacOS, CredentialsDir: s.CredentialsDir}, oauthReadFails{fake}, s.Clk, s.Log)
	before := seatFiles(t, s)
	if _, err := SwitchTo(s, "1", true, false); err == nil || !strings.Contains(err.Error(), "Keychain unreadable") {
		t.Fatalf("err = %v, want the unreadable-credential refusal", err)
	}
	for p, b := range before {
		if got := seatFiles(t, s)[p]; got != b {
			t.Errorf("%s changed: %s", filepath.Base(p), got)
		}
	}
}

// TestASwitchOntoAnEndpointWarnsAboutASecondKey: apiKeyHelper in
// settings.json and ANTHROPIC_API_KEY in the environment are not the
// profile's to remove, and Claude Code sends either as X-Api-Key beside the
// bearer token, so the switch names them: printed, or as JSON warnings.
// Names only, never a value; nothing is said for a switch that applies no
// endpoint.
func TestASwitchOntoAnEndpointWarnsAboutASecondKey(t *testing.T) {
	s := newTestStore(t, nil)
	endpointSeat(t, s)
	m := userSettings()
	m["apiKeyHelper"] = "/opt/me/helper-secret-path"
	writeSettings(t, s, m)
	prev := getenv
	getenv = func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-api03-from-env-secret"
		}
		return ""
	}
	t.Cleanup(func() { getenv = prev })

	ApproveAPIKeySwitch("3")
	res, err := SwitchTo(s, "3", true, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	for _, want := range []string{"sets apiKeyHelper", "ANTHROPIC_API_KEY is set in this environment", "X-Api-Key to gw.example.com"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON result lacks %q: %s", want, b)
		}
	}
	for _, secret := range []string{"helper-secret-path", "from-env-secret", gwKey} {
		if strings.Contains(string(b), secret) {
			t.Errorf("JSON result carries a value (%q): %s", secret, b)
		}
	}
	if got := readSettings(t, s)["apiKeyHelper"]; got != "/opt/me/helper-secret-path" {
		t.Errorf("apiKeyHelper = %v, want it left alone", got)
	}

	out := captureStdout(t, func() {
		if _, err := SwitchTo(s, "1", false, false); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "X-Api-Key") {
		t.Errorf("a switch away warned about a second key:\n%s", out)
	}
	ApproveAPIKeySwitch("3")
	out = captureStdout(t, func() {
		if _, err := SwitchTo(s, "3", false, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "sets apiKeyHelper") || !strings.Contains(out, "ANTHROPIC_API_KEY is set") {
		t.Errorf("human output lacks the warnings:\n%s", out)
	}

	// The direct activation warns the same way.
	switchTo(t, s, "1", false)
	ApproveAPIKeySwitch("3")
	res, err = SwitchTo(s, "3", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), "sets apiKeyHelper") {
		t.Errorf("--force result lacks the warning: %s", b)
	}
}
