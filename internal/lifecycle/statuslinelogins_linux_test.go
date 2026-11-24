// statusline.json names an independent Mythos login once it is signed in,
// without a roster write or a usage change, and still after it is selected
// (DESIGN A113).
package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/statusline"
	"github.com/tyclab/tycswap/internal/store"
)

func TestMythosSignInPublishesItsLogin(t *testing.T) {
	s := fableLifecycleStore(t)
	if err := SetMythos(s, "2", true); err != nil {
		t.Fatal(err)
	}
	dir := store.MythosLoginDir(s.BackupDir(), "b@example.com", "")
	logins := func() map[string]string {
		t.Helper()
		raw, err := os.ReadFile(statusline.Path(s.BackupDir()))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Logins map[string]string `json:"logins"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Logins
	}
	usage := filepath.Join(s.BackupDir(), "cache", "usage.json")
	before, _ := os.ReadFile(usage)
	if _, err := AddMythosLogin(s, loginAs(t, "b@example.com", "", planCreds("at-m2", "rt-m2", "max")), ip(2)); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := logins()[resolved]; got != "b@example.com|" {
		t.Fatalf("after the sign-in: logins[%s] = %q (%v)", resolved, got, logins())
	}
	if err := store.SelectMythosLogin(s.BackupDir(), dir); err != nil {
		t.Fatal(err)
	}
	if got := logins()[resolved]; got != "b@example.com|" {
		t.Fatalf("after selecting it: logins[%s] = %q", resolved, got)
	}
	if after, _ := os.ReadFile(usage); string(after) != string(before) {
		t.Fatal("the usage cache changed in between")
	}
}
