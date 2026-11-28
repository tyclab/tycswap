package lifecycle

import (
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

// An add --slot onto Account-2, the Fable store's account, that a running
// session refuses, on the displaced account or on the one moving in from
// another slot, changes nothing: not the roster, not the Fable store
// (DESIGN A130). With the store empty the account moving in refuses as
// early.
func TestAddRefusedForALiveSessionLeavesTheFableStore(t *testing.T) {
	apiKey := "sk-ant-api03-" + strings.Repeat("z", 40)
	addNew := func(s *store.Store) error {
		_, err := AddAccountFrom(s, loginAs(t, "z@example.com", "", planCreds("at-z", "rt-z", "max")), ip(2), true, nil)
		return err
	}
	moveLive := func(s *store.Store) error {
		_, err := AddAccountFrom(s, LiveLogin, ip(2), true, nil)
		return err
	}
	for _, tc := range []struct {
		name, fable, liveNum, liveEmail string
		act                             func(s *store.Store) error
	}{
		{"add, displaced account", "2", "2", "b@example.com", addNew},
		{"add, moved account", "2", "1", "a@example.com", moveLive},
		{"add, moved account, Fable store empty", "", "1", "a@example.com", moveLive},
		{"add-token, displaced account", "2", "2", "b@example.com", func(s *store.Store) error {
			email, slot := "z@example.com", "2"
			return AddAccountFromToken(s, apiKey, &email, &slot, true)
		}},
		{"add-token, moved account", "2", "4", "d@example.com", func(s *store.Store) error {
			email, slot := "d@example.com", "2"
			return AddAccountFromToken(s, apiKey, &email, &slot, true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := withFableLogins(t, tc.fable)
			startLiveSession(t, s, tc.liveNum, tc.liveEmail)
			before := everyFile(t, s)
			err := tc.act(s)
			if err == nil || !strings.Contains(err.Error(), "live session-mode Claude instance") {
				t.Fatalf("want the live session refusal, got %v", err)
			}
			if st := s.FableStore(); st.On != (tc.fable != "") || st.Live.Slot != tc.fable {
				t.Errorf("FableStore = %+v, want it on %q", st, tc.fable)
			}
			sameFiles(t, "the refused add", before, everyFile(t, s))
		})
	}
}

// A session that starts on the Fable store's account while remove's question
// is open refuses the remove, and the Fable store stays on it (DESIGN A130).
func TestRemoveRefusedAfterTheQuestionLeavesTheFableStore(t *testing.T) {
	s := withFableLogins(t, "2")
	before := everyFile(t, s)
	withPrompter(t, &racingPrompter{t: t, s: s, commit: func() {
		startLiveSession(t, s, "2", "b@example.com")
	}})
	err := RemoveAccount(s, "2", false)
	if errKind(err) != "SessionError" || !strings.Contains(err.Error(), "--remove-account") {
		t.Fatalf("RemoveAccount = %v, want remove's session refusal", err)
	}
	if st := s.FableStore(); !st.On || st.Live.Slot != "2" {
		t.Errorf("FableStore = %+v, want it still on Account-2", st)
	}
	after := everyFile(t, s)
	for p := range after {
		if strings.HasPrefix(p, s.SessionDir("2", "b@example.com")) {
			delete(after, p)
		}
	}
	sameFiles(t, "the refused remove", before, after)
}
