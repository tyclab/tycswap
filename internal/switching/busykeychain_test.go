// A target slot whose backup the Keychain was too busy to read is no empty
// slot (DESIGN A69): the switch stops with the timeout and changes nothing.
package switching

import (
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
)

// busyBackup fails slot's backup read as a busy Keychain does; with waitFor
// set, only once a backup of waitFor was written.
type busyBackup struct {
	credstore.Store
	slot, waitFor string
}

func (b *busyBackup) WriteBackup(num, email, creds string) error {
	if num == b.waitFor {
		b.waitFor = ""
	}
	return b.Store.WriteBackup(num, email, creds)
}

func (b *busyBackup) ReadBackup(num, email string) (string, error) {
	if num == b.slot && b.waitFor == "" {
		return "", cerr.Credential("injected busy read for slot %s", num).Wrap(&keychain.KeychainError{Msg: "security timed out after 15s", Timeout: true})
	}
	return b.Store.ReadBackup(num, email)
}

func TestSwitchStopsOnBusyTargetRead(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "switch"
		if force {
			name = "switch --force"
		}
		t.Run(name, func(t *testing.T) {
			s, backup1, _, live := mcpSwitchFixture(t)
			inner := s.Creds
			s.Creds = &busyBackup{Store: inner, slot: "2"}

			if _, err := SwitchTo(s, "2", true, force); !keychain.IsTimeout(err) {
				t.Fatalf("SwitchTo on a busy Keychain = %v, want the timeout", err)
			}
			if got := readActiveCreds(t, s); got != live {
				t.Errorf("live credential = %q, want it untouched", got)
			}
			assertActiveAccount(t, s, 1)
			if got, _ := inner.ReadBackup("1", "a@x.com"); got != backup1 {
				t.Errorf("Account-1 backup = %q, want it untouched", got)
			}
		})
	}
}

// TestSwitchStopsOnBusyTargetReread: the target's read in step 2, after step
// 1 backed up the outgoing login, stops the switch on its own. The live login
// stays; the outgoing slot keeps the backup step 1 took of it.
func TestSwitchStopsOnBusyTargetReread(t *testing.T) {
	s, _, _, _ := mcpSwitchFixture(t)
	rotated := withMCPOAuth(t, oauthCreds("a1-rotated", "ref1"), "srv|1111", "mcp-live")
	seedLive(t, s, "a@x.com", "", rotated)
	inner := s.Creds
	s.Creds = &busyBackup{Store: inner, slot: "2", waitFor: "1"}

	if _, err := SwitchTo(s, "2", true, false); !keychain.IsTimeout(err) {
		t.Fatalf("SwitchTo with a busy step-2 read = %v, want the timeout", err)
	}
	if got := readActiveCreds(t, s); got != rotated {
		t.Errorf("live credential = %q, want it untouched", got)
	}
	assertActiveAccount(t, s, 1)
	if got, _ := inner.ReadBackup("1", "a@x.com"); oauth.ExtractAccessToken(got) != "a1-rotated" {
		t.Errorf("Account-1 backup = %q, want step 1's backup of the live login", got)
	}
}
