package lifecycle

import (
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/oauth"
)

// add refuses a live login whose tokens Claude Code wiped (DESIGN A130).
func TestAddRefusesAWipedLiveLogin(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), acct{num: "1", email: "alice@example.com", creds: oauthBlob, config: `{"oauthAccount":{"emailAddress":"alice@example.com"}}`})
	seedLiveLogin(t, s, "alice@example.com", "", "", "uuid-a", `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":4102444800000}}`)
	err := AddAccount(s, nil, false, nil)
	if err == nil || cerr.TypeName(err) != "ValidationError" || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("AddAccount = %v, want a validation error asking to sign in again", err)
	}
	if c, _ := s.ReadAccountCredentials("1", "alice@example.com"); c != oauthBlob {
		t.Errorf("Account-1's stored login changed to %q", c)
	}
}

// The refusal of a wiped live login comes before anything is asked or
// changed: an add into the slot of the Fable store's account neither asks to
// overwrite it nor empties the Fable store (DESIGN A130).
func TestAddOfAWipedLiveLoginAsksNothingAndLeavesTheFableStore(t *testing.T) {
	for _, assumeYes := range []bool{true, false} {
		s := withFableLogins(t, "2")
		f, err := s.ForFable()
		if err != nil {
			t.Fatal(err)
		}
		before, _, err := f.Creds.ReadActive()
		if err != nil || before == "" {
			t.Fatal("the Fable store holds no login")
		}
		prompter := &fakePrompter{prompts: []promptResp{{val: "y", ok: true}}}
		withPrompter(t, prompter)
		seedLiveLogin(t, s, "a@example.com", "", "", "uuid-a", `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":4102444800000}}`)
		if err := AddAccount(s, ip(2), assumeYes, nil); err == nil || !strings.Contains(err.Error(), "sign in again") {
			t.Fatalf("assumeYes=%t: AddAccount = %v, want the request to sign in again", assumeYes, err)
		}
		if prompter.pi != 0 {
			t.Errorf("assumeYes=%t: the refused add asked %d question(s)", assumeYes, prompter.pi)
		}
		after, _, err := f.Creds.ReadActive()
		if err != nil {
			t.Fatal(err)
		}
		if !oauth.SameAccountBlock(before, after) {
			t.Errorf("assumeYes=%t: the refused add changed the Fable store: live before=%t, live after=%t", assumeYes, before != "", after != "")
		}
	}
}

// Claude Code wipes the live login while the overwrite question is open: the
// add is refused under the lock, and the Fable store of the displaced account
// is emptied only after that read succeeded, so it stays as it was (DESIGN
// A130).
func TestAddRefusedAfterAWipeDuringTheQuestionLeavesTheFableStore(t *testing.T) {
	s := withFableLogins(t, "2")
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := f.Creds.ReadActive()
	if err != nil || before == "" {
		t.Fatal("the Fable store holds no login")
	}
	seedLiveLogin(t, s, "a@example.com", "", "", "uuid-a", oauthBlob)
	prompter := &racingPrompter{t: t, s: s, commit: func() {
		seedLiveLogin(t, s, "a@example.com", "", "", "uuid-a", `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":4102444800000}}`)
	}}
	withPrompter(t, prompter)
	err = AddAccount(s, ip(2), false, nil)
	if !prompter.fired {
		t.Fatal("the question was not asked")
	}
	if err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("AddAccount = %v, want the request to sign in again", err)
	}
	after, _, err := f.Creds.ReadActive()
	if err != nil {
		t.Fatal(err)
	}
	if !oauth.SameAccountBlock(before, after) {
		t.Errorf("the refused add changed the Fable store: live before=%t, live after=%t", before != "", after != "")
	}
}
