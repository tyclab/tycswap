// End-to-end switches onto an API key on macOS whose ~/.claude.json update
// fails after the key reached the Keychain. The switch rolls the credential
// back only after a credential write that succeeded, so the failed write must
// take its key back out of the Keychain itself: the seat keeps the credential
// it had, and the account the switch left active is the one that is live.
package switching

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
)

// Claude Code's Keychain services: the OAuth credential and the managed key.
const (
	oauthKeychainService   = "Claude Code-credentials"
	managedKeychainService = "Claude Code"
)

// configFailsAfterManagedKey is a Keychain on which a managed key's write
// lands and leaves ~/.claude.json unparseable behind it, so the config update
// that follows in the same credential write fails.
type configFailsAfterManagedKey struct {
	*keychain.Fake
	t      *testing.T
	config string
}

func (k configFailsAfterManagedKey) Set(service, account, password string) error {
	if err := k.Fake.Set(service, account, password); err != nil {
		return err
	}
	if service == managedKeychainService {
		if err := os.WriteFile(k.config, []byte("{"), 0o600); err != nil {
			k.t.Fatal(err)
		}
	}
	return nil
}

// TestFailedSwitchOntoAnAPIKeyLeavesTheKeychainAsItWas_macOS: a switch (the
// normal path's transaction) and a --force activation (the direct path's
// inline rollback) onto an API-key account, from a subscription login and
// from another key, whose config update fails after the key write.
func TestFailedSwitchOntoAnAPIKeyLeavesTheKeychainAsItWas_macOS(t *testing.T) {
	const prevKey = "sk-ant-api03-previous-fixture-0123456789"
	login := oauthCreds("acc-a", "ref-a")
	for _, tc := range []struct {
		name string
		// live is slot 1's credential, live before the switch: a login in
		// the OAuth Keychain item and its shadow file, or a key in the
		// managed item beside a file holding the seat's MCP logins.
		live, file string
	}{
		{"from a subscription login", login, login},
		{"from another API key", prevKey, seatWideFiles[0].file},
	} {
		for _, force := range []bool{false, true} {
			name := tc.name
			if force {
				name += " with --force"
			}
			t.Run(name, func(t *testing.T) {
				s := newTestStore(t, nil)
				slot1 := map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}
				if tc.live == prevKey {
					slot1["kind"] = "api_key"
				}
				writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
					"1": record(slot1),
					"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
				}))
				seedBackup(t, s, "1", oauthSeatEmail, tc.live, "")
				seedBackup(t, s, "2", apiKeySeatEmail, apiKeySeatKey, "")
				seedLive(t, s, oauthSeatEmail, "", tc.file)

				kc := configFailsAfterManagedKey{Fake: keychain.NewFake(), t: t, config: filepath.Join(s.Home, ".claude.json")}
				if tc.live == login {
					kc.Seed(oauthKeychainService, keychain.AccountName(), login)
				} else {
					kc.Seed(managedKeychainService, keychain.AccountName(), prevKey)
				}
				s.Creds = credstore.New(credstore.Config{Platform: platform.MacOS, CredentialsDir: s.CredentialsDir}, kc, s.Clk, s.Log)
				if got := readActiveCreds(t, s); got != tc.live {
					t.Fatalf("precondition: live credential = %q, want slot 1's", got)
				}

				if _, err := SwitchTo(s, "2", true, force); err == nil {
					t.Fatal("SwitchTo(2) succeeded although ~/.claude.json could not be updated")
				}
				item, present, _ := kc.Get(managedKeychainService, keychain.AccountName())
				switch {
				case tc.live == login && present:
					t.Errorf("the failed switch left the key in the managed Keychain item (%q) beside the login", item)
				case tc.live == prevKey && item != prevKey:
					t.Errorf("managed Keychain item = %q after the failed switch, want the previous key", item)
				}
				oauthItem, oauthPresent, _ := kc.Get(oauthKeychainService, keychain.AccountName())
				if wantItem := tc.live == login; oauthPresent != wantItem || (wantItem && oauthItem != login) {
					t.Errorf("OAuth Keychain item = %q (present %v) after the failed switch, want it untouched", oauthItem, oauthPresent)
				}
				if raw, err := os.ReadFile(liveCredentialsPath(s)); err != nil || string(raw) != tc.file {
					t.Errorf("live credentials file = %q (%v) after the failed switch, want it untouched %q", raw, err, tc.file)
				}
				if got := readActiveCreds(t, s); got != tc.live {
					t.Errorf("live credential = %q after the failed switch, want slot 1's %q", got, tc.live)
				}
				assertActiveAccount(t, s, 1)
			})
		}
	}
}

// assertActiveAccount checks the active account sequence.json records.
func assertActiveAccount(t *testing.T, s *store.Store, want int) {
	t.Helper()
	data, err := s.ReadSequence()
	if err != nil || data.ActiveAccountNumber == nil || *data.ActiveAccountNumber != want {
		t.Errorf("active account: %v (%v), want %d", data, err, want)
	}
}

// breakConfigAfterCredentialWrite lets a switch write the target credential,
// then breaks ~/.claude.json, so the switch fails after the credential write
// and rolls back, and the rollback's own config update fails the same way.
type breakConfigAfterCredentialWrite struct {
	credstore.Store
	brk func()
}

func (b breakConfigAfterCredentialWrite) WriteActiveAccount(creds string) error {
	if err := b.Store.WriteActiveAccount(creds); err != nil {
		return err
	}
	b.brk()
	return nil
}

// TestRollbackOntoAnAPIKeyKeepsItLive_macOS: a switch from one API-key account
// to another writes the new key, then fails on ~/.claude.json and rolls back
// onto the first key, whose own config update fails too. The key the rollback
// stored stays: the first key is in the Keychain item and live, as the active
// account says. Undoing that write as a failed switch's write is undone would
// put the second key back.
func TestRollbackOntoAnAPIKeyKeepsItLive_macOS(t *testing.T) {
	const prevKey = "sk-ant-api03-previous-fixture-0123456789"
	for _, mode := range []string{"a corrupt config", "a read-only home"} {
		for _, force := range []bool{false, true} {
			name := mode
			if force {
				name += " with --force"
			}
			t.Run(name, func(t *testing.T) {
				if mode == "a read-only home" && os.Geteuid() == 0 {
					t.Skip("root writes into a read-only directory")
				}
				s := newTestStore(t, nil)
				writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
					"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": "", "kind": "api_key"}),
					"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
				}))
				seedBackup(t, s, "1", oauthSeatEmail, prevKey, "")
				seedBackup(t, s, "2", apiKeySeatEmail, apiKeySeatKey, "")
				seedLive(t, s, oauthSeatEmail, "", seatWideFiles[0].file)
				t.Cleanup(func() { _ = os.Chmod(s.Home, 0o700) })
				brk := func() {
					if mode == "a corrupt config" {
						if err := os.WriteFile(filepath.Join(s.Home, ".claude.json"), []byte("{"), 0o600); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Chmod(s.Home, 0o500); err != nil {
						t.Fatal(err)
					}
				}

				kc := keychain.NewFake()
				kc.Seed(managedKeychainService, keychain.AccountName(), prevKey)
				s.Creds = breakConfigAfterCredentialWrite{
					Store: credstore.New(credstore.Config{Platform: platform.MacOS, CredentialsDir: s.CredentialsDir}, kc, s.Clk, s.Log),
					brk:   brk,
				}
				if got := readActiveCreds(t, s); got != prevKey {
					t.Fatalf("precondition: live credential = %q, want slot 1's key", got)
				}

				if _, err := SwitchTo(s, "2", true, force); err == nil {
					t.Fatal("SwitchTo(2) succeeded although ~/.claude.json broke after the credential write")
				}
				if item, _, _ := kc.Get(managedKeychainService, keychain.AccountName()); item != prevKey {
					t.Errorf("managed Keychain item = %q after the rollback, want slot 1's key", item)
				}
				if got := readActiveCreds(t, s); got != prevKey {
					t.Errorf("live credential = %q after the rollback, want slot 1's key", got)
				}
				assertActiveAccount(t, s, 1)
			})
		}
	}
}
