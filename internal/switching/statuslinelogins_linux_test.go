// statusline.json names the Fable store's account right after a switch or an
// emptying of the store, with no usage change in between (DESIGN A113).
package switching

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/statusline"
	"github.com/tyclab/tycswap/internal/store"
)

func statuslineLogins(t *testing.T, s *store.Store) map[string]string {
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

func TestFableSwitchAndEmptyPublishTheStoresAccount(t *testing.T) {
	s, f := fableSwitchStore(t)
	setFableLive(t, s, planCreds("at-f1", "rt-f1", "max"))
	s.PublishStatusline()
	dir, err := filepath.EvalSymlinks(paths.FableStoreDir(s.BackupDir()))
	if err != nil {
		t.Fatal(err)
	}
	if got := statuslineLogins(t, s)[dir]; got != "one@example.com|org-1" {
		t.Fatalf("before the switch: logins[Fable store] = %q", got)
	}
	usage := filepath.Join(s.BackupDir(), "cache", "usage.json")
	before, _ := os.ReadFile(usage)
	if _, err := SwitchFable(f, "2", FableSwitchOptions{Actor: "hand"}); err != nil {
		t.Fatal(err)
	}
	if got := statuslineLogins(t, s)[dir]; got != "two@example.com|org-2" {
		t.Fatalf("after the switch: logins[Fable store] = %q", got)
	}
	if _, err := EmptyFable(f); err != nil {
		t.Fatal(err)
	}
	if got, present := statuslineLogins(t, s)[dir]; present {
		t.Fatalf("after emptying: logins[Fable store] = %q", got)
	}
	if after, _ := os.ReadFile(usage); string(after) != string(before) {
		t.Fatal("the usage cache changed in between")
	}
}
