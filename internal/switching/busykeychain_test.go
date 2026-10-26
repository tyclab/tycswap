// A target slot whose backup the Keychain was too busy to read is no empty
// slot (DESIGN A69): the switch stops with the timeout and changes nothing.
package switching

import (
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/keychain"
)

// busyBackup fails slot's backup read as a busy Keychain does.
type busyBackup struct {
	credstore.Store
	slot string
}

func (b busyBackup) ReadBackup(num, email string) (string, error) {
	if num == b.slot {
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
			s.Creds = busyBackup{Store: inner, slot: "2"}

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
