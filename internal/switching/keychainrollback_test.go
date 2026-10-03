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
				if got := readActiveCreds(t, s); got != tc.live {
					t.Errorf("live credential = %q after the failed switch, want slot 1's %q", got, tc.live)
				}
				data, err := s.ReadSequence()
				if err != nil || data.ActiveAccountNumber == nil || *data.ActiveAccountNumber != 1 {
					t.Errorf("active account after the failed switch: %v (%v), want 1", data, err)
				}
			})
		}
	}
}
