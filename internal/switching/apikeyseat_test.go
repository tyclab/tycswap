// End-to-end switches on an API-key seat whose credentials file holds nothing
// but seat-wide keys (DESIGN A29). Claude Code writes such a file when an MCP
// server is signed in to, or added with a client secret, while the managed key
// is active: every write to its credential store is a read-modify-write, so
// over an absent file the result is the seat-wide key alone. That file is no
// OAuth login. The managed key is the live credential, the outgoing backup
// keeps it, and the seat-wide keys ride along with both switches.
package switching

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

// apiKeySeatKey is the API-key account's credential: shaped like a managed key
// so the store routes it to Claude Code's managed-key path, and nothing more.
const (
	apiKeySeatKey   = "sk-ant-api03-seat-fixture-0123456789"
	apiKeySeatEmail = "api-key-2@token.local"
	oauthSeatEmail  = "a@x.com"
)

// seatWideFiles are the credentials files Claude Code leaves on an API-key
// seat: an MCP server login, an MCP server added with a client secret, both.
var seatWideFiles = []struct{ name, file string }{
	{"MCP server logins", `{"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live","refreshToken":"mcp-live-refresh","expiresAt":4102444800000}}}`},
	{"MCP client secrets", `{"mcpOAuthClientConfig":{"srv|1111":{"clientSecret":"cs-fixture"}}}`},
	{"both", `{"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live"}},"mcpOAuthClientConfig":{"srv|1111":{"clientSecret":"cs-fixture"}}}`},
}

// apiKeySeat seeds slot 1 (a subscription login, live) and slot 2 (an API-key
// account), switches onto slot 2 as a user does, then writes the credentials
// file Claude Code writes on that seat: seatWide and nothing else.
func apiKeySeat(t *testing.T, s *store.Store, seatWide string) {
	t.Helper()
	ca := oauthCreds("acc-a", "ref-a")
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
		"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
	}))
	seedBackup(t, s, "1", oauthSeatEmail, ca, "")
	seedBackup(t, s, "2", apiKeySeatEmail, apiKeySeatKey, "")
	seedLive(t, s, oauthSeatEmail, "", ca)

	if _, err := SwitchTo(s, "2", true, false); err != nil {
		t.Fatalf("SwitchTo(2): %v", err)
	}
	if got := readActiveCreds(t, s); got != apiKeySeatKey {
		t.Fatalf("precondition: live credential = %q, want the managed key", got)
	}
	if err := os.WriteFile(liveCredentialsPath(s), []byte(seatWide), 0o600); err != nil {
		t.Fatal(err)
	}
}

func liveCredentialsPath(s *store.Store) string {
	return filepath.Join(s.Home, ".claude", ".credentials.json")
}

// liveCredentialsFile returns the decoded live credentials file.
func liveCredentialsFile(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(liveCredentialsPath(s))
	if err != nil {
		t.Fatalf("live credentials file: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("live credentials file is not JSON: %v\n%s", err, raw)
	}
	return m
}

// seatWideOf returns the seat-wide keys of a decoded credential.
func seatWideOf(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"mcpOAuth", "mcpOAuthClientConfig"} {
		if v, ok := m[key]; ok {
			out[key] = v
		}
	}
	return out
}

// assertSlotsIntact checks both slots as stored: slot 2 holds its key byte for
// byte, slot 1 a's login without any seat-wide key, and neither slot retained a
// .prev generation: nothing was displaced that would need it.
func assertSlotsIntact(t *testing.T, s *store.Store, step string) {
	t.Helper()
	if got, _ := s.ReadAccountCredentials("2", apiKeySeatEmail); got != apiKeySeatKey {
		t.Errorf("%s: API-key slot holds %q, want its key", step, got)
	}
	a, _ := s.ReadAccountCredentials("1", oauthSeatEmail)
	var m map[string]any
	if err := json.Unmarshal([]byte(a), &m); err != nil {
		t.Fatalf("%s: slot 1 is not JSON: %v\n%s", step, err, a)
	}
	if oa, _ := m["claudeAiOauth"].(map[string]any); oa["accessToken"] != "acc-a" {
		t.Errorf("%s: slot 1 lost a's login: %s", step, a)
	}
	if sw := seatWideOf(m); len(sw) != 0 {
		t.Errorf("%s: slot 1 stored the seat's %v", step, sw)
	}
	for _, slot := range [][2]string{{"1", oauthSeatEmail}, {"2", apiKeySeatEmail}} {
		if prev, _ := s.Creds.ReadPrev(slot[0], slot[1]); prev != "" {
			t.Errorf("%s: Account-%s retained a .prev generation %q; its backup was displaced", step, slot[0], prev)
		}
	}
}

func TestSwitchFromAnAPIKeySeatWithASeatWideOnlyFileAndBack(t *testing.T) {
	for _, tc := range seatWideFiles {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t, nil)
			apiKeySeat(t, s, tc.file)
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.file), &want); err != nil {
				t.Fatal(err)
			}

			// The managed key is what is live, not the seat-wide file.
			if got := readActiveCreds(t, s); got != apiKeySeatKey {
				t.Errorf("live credential = %q, want the managed key behind the file", got)
			}

			// Away from the key: its slot keeps it, a's login goes live and
			// the seat-wide keys come along.
			if _, err := SwitchTo(s, "1", true, false); err != nil {
				t.Fatalf("SwitchTo(1): %v", err)
			}
			live := liveCredentialsFile(t, s)
			if oa, _ := live["claudeAiOauth"].(map[string]any); oa["accessToken"] != "acc-a" {
				t.Errorf("after the switch to 1: live login = %v, want a's", live["claudeAiOauth"])
			}
			if got := seatWideOf(live); !reflect.DeepEqual(got, want) {
				t.Errorf("after the switch to 1: live seat-wide keys = %v, want %v", got, want)
			}
			assertSlotsIntact(t, s, "after the switch to 1")

			// And back: the key is live again, from its own slot, and the
			// credentials file keeps the seat-wide keys alone.
			if _, err := SwitchTo(s, "2", true, false); err != nil {
				t.Fatalf("SwitchTo(2): %v", err)
			}
			if got := readActiveCreds(t, s); got != apiKeySeatKey {
				t.Errorf("after the switch back: live credential = %q, want the managed key", got)
			}
			if _, err := os.Stat(liveCredentialsPath(s)); err != nil {
				t.Fatalf("after the switch back: the seat-wide keys went with the login: %v", err)
			}
			if back := liveCredentialsFile(t, s); !reflect.DeepEqual(back, want) {
				t.Errorf("after the switch back: credentials file = %v, want the seat-wide keys alone %v", back, want)
			}
			assertSlotsIntact(t, s, "after the switch back")
		})
	}
}

// TestSwitchAwayFromASeatWideOnlyFileWithoutAKeyRefuses: with no managed key
// behind it, a seat-wide-only file reads as no credential at all, and the
// normal switch refuses as for an empty one instead of storing {} (or the
// seat's MCP data) as the outgoing account's credential.
func TestSwitchAwayFromASeatWideOnlyFileWithoutAKeyRefuses(t *testing.T) {
	s := newTestStore(t, nil)
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
		"2": record(map[string]any{"email": "b@x.com", "organizationUuid": ""}),
	}))
	ca := oauthCreds("acc-a", "ref-a")
	seedBackup(t, s, "1", oauthSeatEmail, ca, "")
	seedBackup(t, s, "2", "b@x.com", oauthCreds("acc-b", "ref-b"), "")
	seedLive(t, s, oauthSeatEmail, "", seatWideFiles[0].file)

	if got := readActiveCreds(t, s); got != "" {
		t.Fatalf("live credential = %q, want none (seat-wide keys are no login)", got)
	}
	if _, err := SwitchTo(s, "2", true, false); err == nil {
		t.Fatal("SwitchTo(2) succeeded over a live file with no login in it")
	}
	if got, _ := s.ReadAccountCredentials("1", oauthSeatEmail); got != ca {
		t.Errorf("Account-1 backup = %q, want it untouched", got)
	}
	if raw, _ := os.ReadFile(liveCredentialsPath(s)); string(raw) != seatWideFiles[0].file {
		t.Errorf("live credentials file = %s, want it untouched", raw)
	}
}

// damagedSlotCredentials are what an API-key slot held after a switch away
// from its seat before A29: the switch-time backup took the seat-wide-only
// credentials file for the live login, so the slot kept {} once the seat-wide
// keys were stripped (or the file whole, from a build that did not strip
// them), and the key survived only in .prev.
var damagedSlotCredentials = []struct{ name, creds string }{
	{"empty object", `{}`},
	{"MCP server logins", seatWideFiles[0].file},
	{"MCP client secrets", seatWideFiles[1].file},
}

// damagedAPIKeySeat seeds slot 1 (a subscription login, live, with an MCP
// server login in the live file) and slot 2, an API-key account whose stored
// credential was replaced by damaged as described above.
func damagedAPIKeySeat(t *testing.T, s *store.Store, damaged string) {
	t.Helper()
	ca := oauthCreds("acc-a", "ref-a")
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
		"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
	}))
	seedBackup(t, s, "1", oauthSeatEmail, ca, "")
	seedBackup(t, s, "2", apiKeySeatEmail, apiKeySeatKey, "")
	if err := s.WriteAccountCredentials("2", apiKeySeatEmail, damaged); err != nil {
		t.Fatal(err)
	}
	seedLive(t, s, oauthSeatEmail, "", withMCPOAuth(t, ca, "srv|1111", "mcp-live"))
}

// seatState is everything a refused switch must leave as it found it: the
// live credentials file and config, the managed key, and both slots with
// their .prev generations.
func seatState(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	state := map[string]string{"live credential": readActiveCreds(t, s)}
	for name, path := range map[string]string{
		"live credentials file": liveCredentialsPath(s),
		"live config":           filepath.Join(s.Home, ".claude.json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		state[name] = string(raw)
	}
	for _, slot := range [][2]string{{"1", oauthSeatEmail}, {"2", apiKeySeatEmail}} {
		state["Account-"+slot[0]], _ = s.ReadAccountCredentials(slot[0], slot[1])
		state["Account-"+slot[0]+" .prev"], _ = s.Creds.ReadPrev(slot[0], slot[1])
	}
	return state
}

func assertSeatUnchanged(t *testing.T, s *store.Store, before map[string]string) {
	t.Helper()
	after := seatState(t, s)
	for key, was := range before {
		if after[key] != was {
			t.Errorf("%s changed:\n was %q\n now %q", key, was, after[key])
		}
	}
}

// TestSwitchToADamagedAPIKeySlotRefuses: a stored credential holding nothing
// but seat-wide keys is no credential, so a slot holding one is refused with
// the existing no-credential error, with and without --force, and nothing is
// written. Taking it for an OAuth login would write it live, spliced with the
// seat's MCP data, and clear the managed key: the switch would report success
// over a seat left with no credential at all.
func TestSwitchToADamagedAPIKeySlotRefuses(t *testing.T) {
	for _, tc := range damagedSlotCredentials {
		for _, force := range []bool{false, true} {
			name := tc.name
			if force {
				name += " --force"
			}
			t.Run(name, func(t *testing.T) {
				s := newTestStore(t, nil)
				damagedAPIKeySeat(t, s, tc.creds)
				before := seatState(t, s)

				_, err := SwitchTo(s, "2", true, force)
				if err == nil {
					t.Fatalf("SwitchTo(2) succeeded onto a slot holding %s; live credential now %q", tc.creds, readActiveCreds(t, s))
				}
				if want := "Account-2 has no stored credentials"; !strings.Contains(err.Error(), want) {
					t.Errorf("SwitchTo(2) error = %q, want the no-credential error %q", err, want)
				}
				assertSeatUnchanged(t, s, before)
			})
		}
	}
}

// TestRotationSkipsADamagedAPIKeySlot: the same slot is no rotation or
// auto-switch candidate. The rotation names it as having no stored
// credentials and switches nowhere.
func TestRotationSkipsADamagedAPIKeySlot(t *testing.T) {
	for _, tc := range damagedSlotCredentials {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t, nil)
			damagedAPIKeySeat(t, s, tc.creds)
			before := seatState(t, s)

			if s.AccountIsSwitchable("2") {
				t.Error("AccountIsSwitchable(2) = true for a slot with no credential in it")
			}
			if got := s.SwitchableAccountNumbers(); !reflect.DeepEqual(got, []string{"1"}) {
				t.Errorf("SwitchableAccountNumbers() = %v, want [1]", got)
			}
			res, err := Switch(s, nil, true, nil, nil)
			if err != nil {
				t.Fatalf("Switch: %v", err)
			}
			m, _ := res.(map[string]any)
			if m["switched"] != false || m["reason"] != "no-valid-target" {
				t.Errorf("Switch = %v, want no switch for want of a valid target", m)
			}
			assertSeatUnchanged(t, s, before)
		})
	}
}
