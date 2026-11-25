package web

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
)

// buildlessOps fails the test when a state build reads through the façade.
type buildlessOps struct {
	*fixtureLoginControls
	t *testing.T
}

func (o buildlessOps) ListAccounts(bool, bool, map[string]bool) (any, error) {
	o.t.Error("a state build listed the accounts through the façade")
	return nil, nil
}
func (o buildlessOps) SessionMoves(string) ([]string, error) {
	o.t.Error("a state build asked the façade for session moves")
	return nil, nil
}
func (o buildlessOps) SessionLogin(string) (string, string) {
	o.t.Error("a state build asked the façade for a session login")
	return "", ""
}
func (o buildlessOps) NextStartModel() (string, error) {
	o.t.Error("a state build asked the façade for the next start")
	return "", nil
}

type buildlessAuto struct {
	*fakeAuto
	t *testing.T
}

func (a buildlessAuto) View() AutoView {
	a.t.Error("a state build asked the façade for the Auto view")
	return AutoView{}
}

type buildlessSnapshot struct{ t *testing.T }

func (s buildlessSnapshot) AccountsSnapshot(map[string]bool) *reporting.AccountsSnapshot {
	s.t.Error("a state build took the snapshot through the façade")
	return nil
}

// Each state build asks Deps.BuildReads once for reads of its own and reads
// through them only, the token status and the settings' Auto view included
// (DESIGN A117).
func TestEachStateBuildReadsThroughItsOwnReads(t *testing.T) {
	var builds atomic.Int32
	s, err := New(Deps{
		Facade:   &fakeFacade{snap: sampleSnapshot(), backupDir: t.TempDir()},
		Snapshot: buildlessSnapshot{t},
		Accounts: buildlessOps{&fixtureLoginControls{fakeOps: &fakeOps{}}, t},
		Auto:     buildlessAuto{&fakeAuto{}, t},
		Settings: &fakeSettings{views: sampleSettings()},
		CurrentLogin: func() (string, bool) {
			t.Error("a state build asked the façade for the current login")
			return "", false
		},
		Sessions: func() SessionsView {
			v := sampleSessions()
			v.Claude[0].SessionID = fixtureSession
			return v
		},
		SessionTitle:  func(string, string, string) string { return "" },
		AuthOverrides: func() AuthOverridesView { return AuthOverridesView{} },
		BuildReads: func() StateReads {
			tag := strconv.Itoa(int(builds.Add(1)))
			return StateReads{
				Snapshot: func(map[string]bool) *reporting.AccountsSnapshot {
					snap := sampleSnapshot()
					snap.Accounts[0].Email = "build" + tag + "@example.com"
					return snap
				},
				ListAccounts: func(bool, bool, map[string]bool) (any, error) {
					return map[string]any{"accounts": []any{map[string]any{"number": 1, "tokenStatus": "build " + tag}}}, nil
				},
				CurrentLogin:   func() (string, bool) { return "build" + tag + "@example.com", true },
				AutoView:       func() AutoView { return AutoView{Owner: "build " + tag} },
				NextStartModel: func() (string, error) { return "build " + tag, nil },
				SessionLogin:   func(string) (string, string) { return "build " + tag, tag },
				SessionMoves:   func(string) ([]string, error) { return []string{"build " + tag}, nil },
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1", "2"} {
		st := s.buildState(stateOpts{tokenStatus: true})
		if got := strconv.Itoa(int(builds.Load())); got != want {
			t.Fatalf("after build %s BuildReads was asked %s times", want, got)
		}
		email, tag := "build"+want+"@example.com", "build "+want
		session := st.Sessions.Claude[0]
		for what, got := range map[string]any{
			"row email": st.Accounts[0]["email"], "token status": st.Accounts[0]["tokenStatus"], "current login": st.CurrentLogin.Email,
			"auto owner": st.Auto.Owner, "next start": *st.NextStartModel, "session login": session.Login, "session moves": session.Moves[0],
		} {
			if w := map[bool]string{true: email, false: tag}[what == "row email" || what == "current login"]; got != w {
				t.Errorf("build %s: %s %v, want %q", want, what, got, w)
			}
		}
		if len(st.Settings) == 0 || st.Settings[0].Applies != "Applies at the next scheduled check." {
			t.Errorf("build %s: settings %+v, want them as a scheduler named by the build's Auto view runs the ticks", want, st.Settings)
		}
	}
}
