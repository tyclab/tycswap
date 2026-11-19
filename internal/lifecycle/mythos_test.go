package lifecycle

import (
	"os"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

// The Mythos mark is set and cleared under the roster lock like the start
// login's, keeps every other field, and a no-op writes nothing (DESIGN A106).
func TestMythosMarksPreserveMetadata(t *testing.T) {
	s := newStore(t)
	seed(t, s, nil, acct{num: "1", email: "a@example.com", alias: "work", disabled: true, startLogin: true})
	if err := SetMythos(s, "WORK", true); err != nil {
		t.Fatal(err)
	}
	if !s.MythosCapable("1") {
		t.Fatal("mark not set")
	}
	r := rec(t, readSeq(t, s), "1")
	if !r.boolVal("startLogin") || !r.boolVal("disabled") || r.str("alias") != "work" {
		t.Fatalf("metadata changed: %+v", r.vals)
	}
	before, _ := os.ReadFile(s.SequenceFile)
	if err := SetMythos(s, "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(s.SequenceFile); string(before) != string(after) {
		t.Fatal("no-op rewrote sequence")
	}
	if err := SetMythos(s, "1", false); err != nil {
		t.Fatal(err)
	}
	if s.MythosCapable("1") || rec(t, readSeq(t, s), "1").has("mythos") || !s.IsStartLogin("1") {
		t.Fatal("mark not removed, or the start mark with it")
	}
	if err := SetMythos(s, "9", true); err == nil {
		t.Fatal("unknown account accepted")
	}
}

// The mark follows the identity across a re-add, a move and a swap, and a
// new identity in the slot does not inherit it (DESIGN A90, A106).
func TestMythosMarkFollowsIdentity(t *testing.T) {
	for _, name := range []string{"re-add", "move", "swap", "replace"} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			seed(t, s, nil, switchable("1", "a@example.com"), switchable("2", "b@example.com"))
			if err := SetMythos(s, "1", true); err != nil {
				t.Fatal(err)
			}
			seedLiveLogin(t, s, "a@example.com", "", "", "", `{"claudeAiOauth":{"accessToken":"fake","refreshToken":"fake-refresh"}}`)
			want := "1"
			var err error
			switch name {
			case "re-add":
				err = AddAccount(s, nil, true, nil)
			case "move":
				_, _, _, err = MoveAccount(s, "1", "3")
				want = "3"
			case "swap":
				_, _, err = SwapAccounts(s, "1", "2")
				want = "2"
			case "replace":
				seedLiveLogin(t, s, "new@example.com", "", "", "", `{"claudeAiOauth":{"accessToken":"fake"}}`)
				err = AddAccount(s, ip(1), true, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			data := readSeq(t, s)
			if name == "replace" {
				if store.MythosFrom(data, "1") {
					t.Fatal("mark leaked to a new identity")
				}
				return
			}
			for num := range data.Accounts {
				if store.MythosFrom(data, num) != (num == want) {
					t.Fatalf("mark on %s = %v, want it on %s only", num, store.MythosFrom(data, num), want)
				}
			}
		})
	}
}
