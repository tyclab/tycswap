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
// endpoint is written: a base URL of their own (a corporate proxy), an env key
// beside it, hooks and permissions.
func userSettings() map[string]any {
	return map[string]any{
		"theme":       "dark",
		"permissions": map[string]any{"allow": []any{"Bash(ls)"}},
		"hooks":       map[string]any{"SessionStart": []any{}},
		"env":         map[string]any{"ANTHROPIC_BASE_URL": "https://user-proxy.example", "FOO": "bar"},
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
// url/key: the two keys in settings.json beside every key of the user's, no
// key and no login in Claude Code's credential store (the MCP login stays),
// the account's identity live, a record of the prior settings.
func assertOnEndpoint(t *testing.T, s *store.Store, email, url, key string, slot int) {
	t.Helper()
	got := readSettings(t, s)
	want := userSettings()
	settingsEnv(want)["ANTHROPIC_BASE_URL"] = url
	settingsEnv(want)["ANTHROPIC_AUTH_TOKEN"] = key
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
		_, err := SwitchTo(s, "1", true, false)
		if err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("err = %v, want the corrupt-record refusal", err)
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
	if !strings.Contains(out, "now sends its requests to gw.example.com") || !strings.Contains(out, APIKeyRestartNote) {
		t.Errorf("follow-up onto the endpoint:\n%s", out)
	}
	out = captureStdout(t, func() {
		if _, err := SwitchTo(s, "1", false, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "no longer sends its requests") || !strings.Contains(out, APIKeyRestartNote) {
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
