package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/store"
)

func TestMythosLoginFollowsIdentityWhenAccountChanges(t *testing.T) {
	for _, operation := range []string{"remove", "replace", "move"} {
		t.Run(operation, func(t *testing.T) {
			s := newStore(t)
			seed(t, s, ip(1), switchable("1", "a@example.com"), switchable("2", "b@example.com"))
			_, email, org, err := s.ResolveAccount("2")
			if err != nil {
				t.Fatal(err)
			}
			dir := store.MythosLoginDir(s.BackupDir(), email, org)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(dir, ccfile.CredentialsFileName)
			if err := os.WriteFile(filename, []byte(planCreds("access", "refresh", "max")), 0o600); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "remove":
				err = RemoveAccount(s, "2", true)
			case "replace":
				_, err = AddAccountFrom(s, loginAs(t, email, "other-org", planCreds("new", "new-r", "max")), ip(2), true, nil)
			case "move":
				_, err = AddAccountFrom(s, loginAs(t, email, org, planCreds("new", "new-r", "max")), ip(3), true, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filename)
			if operation == "move" && err != nil || operation != "move" && !os.IsNotExist(err) {
				t.Fatalf("independent credential after %s: %v", operation, err)
			}
		})
	}
}
