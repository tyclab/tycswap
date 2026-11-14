package lifecycle

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

func TestStartLoginMarksPreserveMetadata(t *testing.T) {
	s := newStore(t)
	seed(t, s, nil, acct{num: "1", email: "a@example.com", alias: "work", disabled: true})
	data := readSeq(t, s)
	r := rec(t, data, "1")
	r.set("futureField", map[string]any{"retained": true})
	if err := putRecord(data, "1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
	if err := SetStartLogin(s, "WORK", true); err != nil {
		t.Fatal(err)
	}
	if got := s.StartLoginNumbers(); !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("marks %v", got)
	}
	r = rec(t, readSeq(t, s), "1")
	if !r.boolVal("startLogin") || !r.boolVal("disabled") || r.str("alias") != "work" || !r.has("futureField") {
		t.Fatalf("metadata changed: %+v", r.vals)
	}
	before, _ := os.ReadFile(s.SequenceFile)
	if err := SetStartLogin(s, "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.SequenceFile)
	if string(before) != string(after) {
		t.Fatal("no-op rewrote sequence")
	}
	if err := SetStartLogin(s, "1", false); err != nil {
		t.Fatal(err)
	}
	if s.IsStartLogin("1") || rec(t, readSeq(t, s), "1").has("startLogin") {
		t.Fatal("mark not removed")
	}
}

func TestStartLoginConcurrentWritesAndRefusals(t *testing.T) {
	s := newStore(t)
	seed(t, s, nil, acct{num: "1", email: "a@example.com"}, acct{num: "2", email: "a@example.com", org: "org"})
	if err := SetStartLogin(s, "a@example.com", true); err == nil {
		t.Fatal("ambiguous identity accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"1", "2"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); errs <- SetStartLogin(s, id, true) }(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := s.StartLoginNumbers(); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("lost write: %v", got)
	}
	if err := os.WriteFile(s.SequenceFile, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetStartLogin(s, "1", false); err == nil {
		t.Fatal("corrupt roster overwritten")
	}
	b, _ := os.ReadFile(s.SequenceFile)
	if string(b) != "{broken" {
		t.Fatal("corrupt roster modified")
	}
}

func TestStartLoginMetadataFollowsIdentity(t *testing.T) {
	for _, name := range []string{"re-add", "re-login", "slot re-add", "migrate", "add-token", "token slot", "token migrate", "move", "swap", "replace", "replace org"} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			a := switchable("1", "a@example.com")
			a.alias, a.disabled, a.startLogin = "work", true, true
			seed(t, s, nil, a, switchable("2", "b@example.com"))
			seedLiveLogin(t, s, "a@example.com", "", "", "", `{"claudeAiOauth":{"accessToken":"fake","refreshToken":"fake-refresh"}}`)
			want := "1"
			var err error
			switch name {
			case "re-add":
				err = AddAccount(s, nil, true, nil)
			case "re-login":
				dir := t.TempDir()
				for name, text := range map[string]string{
					".claude.json":      `{"oauthAccount":{"emailAddress":"a@example.com","organizationUuid":""}}`,
					".credentials.json": `{"claudeAiOauth":{"accessToken":"fake","refreshToken":"fake-refresh"}}`,
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				_, err = AddAccountFrom(s, LoginDir(dir, nil), nil, true, nil)
			case "slot re-add":
				err = AddAccount(s, ip(1), true, nil)
			case "migrate":
				err = AddAccount(s, ip(3), true, nil)
				want = "3"
			case "add-token":
				email := "a@example.com"
				err = AddAccountFromToken(s, "setup-fake", &email, nil, true)
			case "token slot":
				email, slot := "a@example.com", "1"
				err = AddAccountFromToken(s, "setup-fake", &email, &slot, true)
			case "token migrate":
				email, slot := "a@example.com", "3"
				err = AddAccountFromToken(s, "setup-fake", &email, &slot, true)
				want = "3"
			case "move":
				_, _, _, err = MoveAccount(s, "1", "3")
				want = "3"
			case "swap":
				_, _, err = SwapAccounts(s, "1", "2")
				want = "2"
			case "replace org":
				seedLiveLogin(t, s, "a@example.com", "different-org", "", "", `{"claudeAiOauth":{"accessToken":"fake"}}`)
				err = AddAccount(s, ip(1), true, nil)
			case "replace":
				seedLiveLogin(t, s, "new@example.com", "", "", "", `{"claudeAiOauth":{"accessToken":"fake"}}`)
				err = AddAccount(s, ip(1), true, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			r := rec(t, readSeq(t, s), want)
			if name == "replace" || name == "replace org" {
				if r.boolVal("startLogin") || r.boolVal("disabled") || r.str("alias") != "" {
					t.Fatalf("metadata leaked: %+v", r.vals)
				}
				return
			}
			if !r.boolVal("startLogin") || !r.boolVal("disabled") || r.str("alias") != "work" {
				t.Fatalf("metadata lost: %+v", r.vals)
			}
			data := readSeq(t, s)
			for num := range data.Accounts {
				if num != want && store.StartLoginFrom(data, num) {
					t.Fatalf("mark leaked to %s", num)
				}
			}
		})
	}
}
