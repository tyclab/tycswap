// Tests that what placing sessions reads that does not depend on the session
// is read once per dashboard state build, and that the tick's moves stay
// decided per session (DESIGN A117).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/store/ownertest"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/web"
	"github.com/tyclab/tycswap/internal/wincred"
)

// costFixture is the ownership fixture as the operator's box has it: eight
// accounts, each a start login, the Fable store on account 6, Mythos logins
// on 7 and 8, auto-switch on; padKiB grows the four identity files.
func costFixture(t testing.TB, padKiB int) (*core.Switcher, *ownertest.Fixture) {
	t.Helper()
	testutil.IsolateHome(t)
	testutil.Setenv(t, "NO_COLOR", "1")
	sw, err := newSwitcher(store.Options{Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	f := ownertest.Build(t, sw.Store, ownertest.Options{Accounts: 8, PadKiB: padKiB, Fable: true, StartLogins: true, Mythos: []string{"7", "8"}})
	if err := os.WriteFile(settings.SettingsPath(sw.BackupDir()), []byte(`{"schemaVersion":1,"autoswitch":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return sw, f
}

// linked is a session of this process bound to a link.
type linked struct{ id, link, target string }

func costSessionID(i int) string { return fmt.Sprintf("5e551011-2222-4333-8444-%012d", i) }

// linkSession binds session id, running model, to a new link to target, with
// wanted as its held choice ("" for none).
func linkSession(t testing.TB, sw *core.Switcher, id, model, target, wanted string) linked {
	t.Helper()
	root := sw.BackupDir()
	link, err := reporting.CreateSessionLink(root, target)
	if err != nil {
		t.Fatal(err)
	}
	scope := ""
	switch {
	case target == paths.FableStoreDir(root):
		scope = "fable"
	case filepath.Dir(target) == store.MythosLoginRoot(root):
		scope = "mythos/" + filepath.Base(target)
	}
	if err := reporting.BindSessionLink(root, id, link, model, scope, sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	if wanted != "" {
		if err := reporting.UpdateSessionLink(root, id, link, sw.Clk.Now(), func(rec *reporting.SessionLink) error {
			rec.Wanted = wanted
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return linked{id: id, link: link, target: target}
}

// linkSessions binds n sessions in turn to the default login (Opus), the
// Fable store (Fable), account 7's Mythos login and the default login (Fable).
func linkSessions(t testing.TB, sw *core.Switcher, f *ownertest.Fixture, n int) []linked {
	t.Helper()
	turn := []struct{ model, target string }{
		{"claude-opus-5-5", sw.DefaultProfileDir()},
		{"claude-fable-5-2", paths.FableStoreDir(sw.BackupDir())},
		{"claude-mythos-5-2", f.Mythos["7"]},
		{"claude-fable-5-2", sw.DefaultProfileDir()},
	}
	var out []linked
	for i := range n {
		out = append(out, linkSession(t, sw, costSessionID(i), turn[i%len(turn)].model, turn[i%len(turn)].target, ""))
	}
	return out
}

// listedSessions is the process probe the dashboard would make with these
// sessions running in the default profile.
func listedSessions(sessions []linked, dir string) func() web.SessionsView {
	return func() web.SessionsView {
		v := web.SessionsView{ConfigDir: map[int]string{}, Profile: map[int]string{}}
		for i, s := range sessions {
			pid := 400000 + i
			v.Claude = append(v.Claude, procdetect.ClaudeSession{PID: pid, SessionID: s.id, CWD: "/work", Kind: "interactive", Entrypoint: "cli"})
			v.ConfigDir[pid], v.Profile[pid] = dir, ""
		}
		return v
	}
}

func noTitle(string, string, string) string { return "" }

// costServer is the dashboard server newDashboard wires over sw, without
// Codex, not started, listing sessions and titling them with title.
func costServer(t testing.TB, sw *core.Switcher, sessions func() web.SessionsView, title func(dir, cwd, id string) string) *web.Server {
	t.Helper()
	auto := newAutoFacade(sw, nil)
	auto.managedBy = testOwner
	d := &dashboard{sw: sw, auto: auto}
	deps := dashboardDeps(context.Background(), d, nil, 3600, false, ioStreams{in: strings.NewReader(""), out: io.Discard, err: io.Discard})
	deps.Sessions, deps.SessionTitle, deps.UpdatesOwnSchedule, deps.RemoteToken = sessions, title, true, "remote-test-token"
	srv, err := web.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	d.srv = srv
	return srv
}

// serveCost serves srv on loopback until the test ends; it returns a call
// with the remote token.
func serveCost(t *testing.T, srv *web.Server) func(method, path, body string) (int, string) {
	t.Helper()
	launch, err := srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	base := launch[:strings.Index(launch, "/?token=")]
	return func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer remote-test-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
}

// countFableStartSnapshots counts readFableStartSnapshot until the test ends;
// each, when set, is told the count so far as it happens.
func countFableStartSnapshots(t testing.TB, each func(n int32)) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	prev := observeFableStartSnapshot
	observeFableStartSnapshot = func() {
		got := n.Add(1)
		if each != nil {
			each(got)
		}
	}
	t.Cleanup(func() { observeFableStartSnapshot = prev })
	return &n
}

// stateSessions is the state's sessions by ID.
func stateSessions(st web.State) map[string]web.ClaudeSessionJSON {
	out := map[string]web.ClaudeSessionJSON{}
	for _, c := range st.Sessions.Claude {
		out[c.SessionID] = c
	}
	return out
}

// requirePlaced fails unless the build named every session's login and
// offered it moves or said why not.
func requirePlaced(t testing.TB, st web.State, sessions []linked) {
	t.Helper()
	got := stateSessions(st)
	for _, s := range sessions {
		c, ok := got[s.id]
		if !ok || c.Login == "" || len(c.Moves) == 0 && c.MovesError == "" {
			t.Fatalf("session %s in the state: %+v (listed %v)", s.id, c, ok)
		}
	}
}

// A whole dashboard state build, through the server as newDashboard wires
// it, reads each identity file and scans each profile at most once, takes at
// most one Fable start snapshot, and costs with 12 linked sessions what it
// costs with 4 (DESIGN A117).
func TestStateBuildReadsEachInputOncePerBuild(t *testing.T) {
	type cost struct{ identity, pids, roster, snapshots int }
	costs := map[int]cost{}
	for _, n := range []int{1, 4, 12} {
		t.Run(fmt.Sprintf("%d sessions", n), func(t *testing.T) {
			sw, f := costFixture(t, 0)
			sessions := linkSessions(t, sw, f, n)
			srv := costServer(t, sw, listedSessions(sessions, sw.DefaultProfileDir()), noTitle)
			srv.Snapshot()
			counts := ownertest.CountReads(t)
			snaps := countFableStartSnapshots(t, nil)
			st := srv.Snapshot()
			identity, pids, roster := counts.Totals()
			costs[n] = cost{identity, pids, roster, int(snaps.Load())}
			t.Logf("%d sessions: %+v", n, costs[n])
			requirePlaced(t, st, sessions)
			counts.AtMostOnce(t, "one state build")
			if got := snaps.Load(); got > 1 {
				t.Fatalf("one state build took %d Fable start snapshots, want at most one", got)
			}
		})
	}
	if costs[12] != costs[4] {
		t.Errorf("a state build costs %+v with 12 linked sessions and %+v with 4", costs[12], costs[4])
	}
	one, four := costs[1], costs[4]
	if one.identity != four.identity || one.pids != four.pids || one.snapshots != four.snapshots {
		t.Errorf("a state build costs %+v with one linked session and %+v with 4", one, four)
	}
}

// The state with each row's token status, as an SSE subscriber or the tray
// asks for it, is one read operation as well.
func TestStateBuildWithTokenStatusReadsEachInputOnce(t *testing.T) {
	sw, f := costFixture(t, 0)
	sessions := linkSessions(t, sw, f, 12)
	call := serveCost(t, costServer(t, sw, listedSessions(sessions, sw.DefaultProfileDir()), noTitle))
	call(http.MethodGet, "/api/state?tokenStatus=1", "")
	counts := ownertest.CountReads(t)
	snaps := countFableStartSnapshots(t, nil)
	code, body := call(http.MethodGet, "/api/state?tokenStatus=1", "")
	if code != http.StatusOK || !strings.Contains(body, `"tokenStatus"`) {
		t.Fatalf("state with token status: %d %.300s", code, body)
	}
	counts.AtMostOnce(t, "one state build with token status")
	if got := snaps.Load(); got > 1 {
		t.Fatalf("one state build took %d Fable start snapshots, want at most one", got)
	}
}

// pausedBuild starts a state build that stops when it titles its second
// session, after it has placed the first; release lets it finish.
func pausedBuild(t *testing.T, sw *core.Switcher, sessions []linked) (srv *web.Server, built <-chan web.State, release func()) {
	t.Helper()
	var titles atomic.Int32
	reached, resume := make(chan struct{}), make(chan struct{})
	srv = costServer(t, sw, listedSessions(sessions, sw.DefaultProfileDir()), func(string, string, string) string {
		if titles.Add(1) == 2 {
			close(reached)
			<-resume
		}
		return ""
	})
	out := make(chan web.State, 1)
	go func() { out <- srv.Snapshot() }()
	<-reached
	released := false
	release = func() {
		if !released {
			released = true
			close(resume)
		}
	}
	t.Cleanup(release)
	return srv, out, release
}

// rowReason is account num's ownership block reason in the state.
func rowReason(st web.State, num int) string {
	for _, row := range st.Accounts {
		if row["number"] == num {
			reason, _ := row["ownershipBlockReason"].(string)
			return reason
		}
	}
	return "<no row>"
}

// Two builds, the second started while the first is under way and after
// identity files and the default login changed: the second reports what
// holds now, whatever the first read; so does a build that starts after the
// first ended (DESIGN A117).
func TestStateBuildsDoNotShareTheirReads(t *testing.T) {
	change := func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
		t.Helper()
		// Account 2's running profile signs in as account 4, the default
		// config names account 7 and the default login holds a credential no
		// account's backup holds.
		writeFile(t, filepath.Join(f.Legacy["2"], ".claude.json"), ownertest.Identity(f.Emails["4"], "", 0))
		writeFile(t, sw.DefaultConfigPath(), ownertest.Identity(f.Emails["7"], "", 0))
		if err := sw.Creds.WriteActive(ownertest.Creds("unmatched")); err != nil {
			t.Fatal(err)
		}
	}
	reportsChange := func(t *testing.T, st web.State, f *ownertest.Fixture, fable linked) {
		t.Helper()
		if st.CurrentLogin == nil || st.CurrentLogin.Email != f.Emails["7"] {
			t.Errorf("current login %+v, want account 7's", st.CurrentLogin)
		}
		if reason := rowReason(st, 4); !strings.Contains(reason, "Account-4 has a running session profile") {
			t.Errorf("account 4's ownership block %q, want the running profile that signed in as it", reason)
		}
		if got := stateSessions(st)[fable.id]; slices.Contains(got.Moves, "default") {
			t.Errorf("Fable store session %+v, want no move to the unverified default login", got)
		}
	}
	reportsBefore := func(t *testing.T, st web.State, fable linked) {
		t.Helper()
		if got := stateSessions(st)[fable.id]; !slices.Contains(got.Moves, "default") || rowReason(st, 4) != "" {
			t.Fatalf("fixture: Fable store session %+v, account 4's block %q; want a move to the default login and account 4 free", got, rowReason(st, 4))
		}
	}
	t.Run("overlapping", func(t *testing.T) {
		sw, f := costFixture(t, 0)
		sessions := linkSessions(t, sw, f, 4)
		srv, first, release := pausedBuild(t, sw, sessions)
		change(t, sw, f)
		second := srv.Snapshot()
		release()
		<-first
		reportsChange(t, second, f, sessions[1])
	})
	t.Run("one after the other", func(t *testing.T) {
		sw, f := costFixture(t, 0)
		sessions := linkSessions(t, sw, f, 4)
		srv := costServer(t, sw, listedSessions(sessions, sw.DefaultProfileDir()), noTitle)
		reportsBefore(t, srv.Snapshot(), sessions[1])
		change(t, sw, f)
		reportsChange(t, srv.Snapshot(), f, sessions[1])
	})
}

// While a state build is under way, a switch and a session move through the
// dashboard read what holds now, not what the build read (DESIGN A117).
func TestMutationsDuringAStateBuildReadAfresh(t *testing.T) {
	t.Run("switch guard", func(t *testing.T) {
		sw, f := costFixture(t, 0)
		sessions := linkSessions(t, sw, f, 4)
		srv, first, release := pausedBuild(t, sw, sessions)
		call := serveCost(t, srv)
		// Account 2's profile, running its session, signs in as account 4.
		if err := os.WriteFile(filepath.Join(f.Legacy["2"], ".claude.json"), []byte(ownertest.Identity(f.Emails["4"], "", 0)), 0o600); err != nil {
			t.Fatal(err)
		}
		code, body := call(http.MethodPost, "/api/switch/claude:4", "")
		release()
		<-first
		if code == http.StatusOK || !strings.Contains(body, "Account-4 has a running session profile") {
			t.Fatalf("switch to account 4: %d %s; want the running profile that signed in as it", code, body)
		}
		if got := liveAccount(t, sw); got != f.Emails["1"] {
			t.Fatalf("live login %s, want account 1's", got)
		}
	})
	t.Run("session move", func(t *testing.T) {
		sw, f := costFixture(t, 0)
		sessions := linkSessions(t, sw, f, 4)
		srv, first, release := pausedBuild(t, sw, sessions)
		call := serveCost(t, srv)
		if err := sw.Creds.WriteActive(ownertest.Creds("unmatched")); err != nil {
			t.Fatal(err)
		}
		code, body := call(http.MethodPost, "/api/sessions/"+sessions[1].id+"/use", `{"store":"default"}`)
		release()
		<-first
		if code == http.StatusOK || !strings.Contains(body, "cannot verify the current default login") {
			t.Fatalf("move to the default login: %d %s; want the unverified login refused", code, body)
		}
		requireLinkTarget(t, sessions[1].link, paths.FableStoreDir(sw.BackupDir()))
	})
}

// sessionAnswer is what the dashboard shows of a session's login and moves.
type sessionAnswer struct {
	Moves               []string
	Error, Login, Owner string
}

// perSession is a session's answer computed alone, as the state build asked
// for it before DESIGN A117.
func perSession(sw *core.Switcher, id string) sessionAnswer {
	var a sessionAnswer
	moves, err := sessionMoveChoices(sw, id)
	a.Moves = moves
	var ce *cerr.Error
	if err != nil && !(len(moves) == 0 && errors.As(err, &ce) && ce.Kind == cerr.KindAccountNotFound) {
		a.Error = err.Error()
	}
	a.Login, a.Owner = sessionLogin(sw, id)
	return a
}

func sameAnswer(a, b sessionAnswer) bool {
	return slices.Equal(a.Moves, b.Moves) && a.Error == b.Error && a.Login == b.Login && a.Owner == b.Owner
}

// equalitySessions links a session to each kind of login and choice: the
// default login (Opus, Fable, Opus holding Fable, Mythos holding Mythos), the
// Fable store (Fable, Opus holding Fable), account 7's Mythos login, account
// 8's whose client is refreshing, and a Mythos login no account owns.
func equalitySessions(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) map[string]linked {
	t.Helper()
	root, def, fable := sw.BackupDir(), sw.DefaultProfileDir(), paths.FableStoreDir(sw.BackupDir())
	stranger := store.MythosLoginDir(root, "stranger@example.com", "")
	writeFile(t, filepath.Join(stranger, ".credentials.json"), ownertest.Creds("stranger"))
	if err := os.Mkdir(filepath.Join(f.Mythos["8"], ".oauth_refresh.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	out := map[string]linked{}
	for i, s := range []struct{ name, model, target, wanted string }{
		{"default opus", "claude-opus-5-5", def, ""},
		{"default fable", "claude-fable-5-2", def, ""},
		{"default opus holding fable", "claude-opus-5-5", def, "fable"},
		{"default mythos holding mythos", "claude-mythos-5-2", def, "mythos"},
		{"fable store fable", "claude-fable-5-2", fable, ""},
		{"fable store opus holding fable", "claude-opus-5-5", fable, "fable"},
		{"mythos 7", "claude-mythos-5-2", f.Mythos["7"], ""},
		{"mythos 8 refreshing", "claude-mythos-5-2", f.Mythos["8"], ""},
		{"mythos owner unknown", "claude-mythos-5-2", stranger, ""},
	} {
		out[s.name] = linkSession(t, sw, costSessionID(100+i), s.model, s.target, s.wanted)
	}
	return out
}

// setUsage sets num's cached 7-day reading to pct.
func setUsage(t *testing.T, sw *core.Switcher, num string, pct float64) {
	t.Helper()
	path := filepath.Join(sw.BackupDir(), "cache", "usage.json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(readOrAbsent(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	row := doc["accounts"].(map[string]any)[num].(map[string]any)
	row["lastGood"].(map[string]any)["seven_day"].(map[string]any)["pct"] = pct
	raw, _ := json.Marshal(doc)
	writeFile(t, path, string(raw))
}

func unverifyDefault(t *testing.T, sw *core.Switcher, _ *ownertest.Fixture) {
	t.Helper()
	if err := sw.Creds.WriteActive(ownertest.Creds("unmatched")); err != nil {
		t.Fatal(err)
	}
}

func emptyFableStore(t *testing.T, sw *core.Switcher, _ *ownertest.Fixture) {
	t.Helper()
	if err := os.Remove(filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json")); err != nil {
		t.Fatal(err)
	}
}

func spendMythos(t *testing.T, sw *core.Switcher, _ *ownertest.Fixture) {
	t.Helper()
	setUsage(t, sw, "7", 100)
	setUsage(t, sw, "8", 100)
}

// In one state build each session's moves, the reason it has none and its
// login and owner are what that session alone is answered (DESIGN A117),
// over every kind of login and choice, in states that refuse each
// destination for a reason of its own.
func TestStateBuildAnswersEachSessionAsAlone(t *testing.T) {
	for _, variant := range []struct {
		name    string
		prepare func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture)
		want    map[string]sessionAnswer
	}{
		{name: "as built", want: asBuiltAnswers},
		{name: "default login unverified", prepare: unverifyDefault},
		{name: "default login's account at its limit", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			setUsage(t, sw, "1", 100)
		}},
		{name: "no start login in the Fable pool", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			setRecord(t, sw, "6", "startLogin", false)
			setRecord(t, sw, "7", "startLogin", false)
		}},
		{name: "Fable store empty", prepare: emptyFableStore},
		{name: "Mythos logins spent", prepare: spendMythos},
		{name: "Mythos logins spent, Fable store empty", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			spendMythos(t, sw, f)
			emptyFableStore(t, sw, f)
		}, want: noMythosNoFableAnswers},
		{name: "Mythos logins spent, default login unverified", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			spendMythos(t, sw, f)
			unverifyDefault(t, sw, f)
		}, want: noMythosNoDefaultAnswers},
		{name: "every login refused", prepare: func(t *testing.T, sw *core.Switcher, f *ownertest.Fixture) {
			spendMythos(t, sw, f)
			emptyFableStore(t, sw, f)
			setRecord(t, sw, "1", "disabled", true)
		}, want: everyLoginRefusedAnswers},
	} {
		t.Run(variant.name, func(t *testing.T) {
			sw, f := costFixture(t, 0)
			sessions := equalitySessions(t, sw, f)
			if variant.prepare != nil {
				variant.prepare(t, sw, f)
			}
			var listed []linked
			for _, s := range sessions {
				listed = append(listed, s)
			}
			slices.SortFunc(listed, func(a, b linked) int { return strings.Compare(a.id, b.id) })
			built := stateSessions(costServer(t, sw, listedSessions(listed, sw.DefaultProfileDir()), noTitle).Snapshot())
			for name, s := range sessions {
				c := built[s.id]
				got := sessionAnswer{Moves: c.Moves, Error: c.MovesError, Login: c.Login, Owner: c.AccountNumber}
				alone := perSession(sw, s.id)
				t.Logf("%q: {Moves: %#v, Error: %q, Login: %q, Owner: %q},", name, got.Moves, got.Error, got.Login, got.Owner)
				if !sameAnswer(got, alone) {
					t.Errorf("%s in one state build %+v, alone %+v", name, got, alone)
				}
				if want, ok := variant.want[name]; ok && !sameAnswer(got, want) {
					t.Errorf("%s = %+v, want %+v", name, got, want)
				}
			}
		})
	}
}

// The sessions' answers as the state build gave them before DESIGN A117.
var (
	asBuiltAnswers = map[string]sessionAnswer{
		"default fable":                  {Moves: []string{"mythos", "fable"}, Error: "", Login: "default login", Owner: "1"},
		"default mythos holding mythos":  {Moves: []string{"mythos", "fable"}, Error: "", Login: "default login", Owner: "1"},
		"default opus holding fable":     {Moves: []string{"mythos", "fable"}, Error: "", Login: "default login", Owner: "1"},
		"default opus":                   {Moves: []string{"mythos", "fable"}, Error: "", Login: "default login", Owner: "1"},
		"fable store fable":              {Moves: []string{"mythos", "default"}, Error: "", Login: "Fable store", Owner: "6"},
		"fable store opus holding fable": {Moves: []string{"mythos", "default"}, Error: "", Login: "Fable store", Owner: "6"},
		"mythos 7":                       {Moves: []string{"fable", "default"}, Error: "", Login: "Mythos login", Owner: "7"},
		"mythos 8 refreshing":            {Moves: []string(nil), Error: "The session login is refreshing.", Login: "Mythos login", Owner: "8"},
		"mythos owner unknown":           {Moves: []string{"mythos", "fable", "default"}, Error: "", Login: "Mythos login", Owner: "unknown"},
	}
	noMythosNoFableAnswers = map[string]sessionAnswer{
		"default fable":                  {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)", Login: "default login", Owner: "1"},
		"default mythos holding mythos":  {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)", Login: "default login", Owner: "1"},
		"default opus holding fable":     {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)", Login: "default login", Owner: "1"},
		"default opus":                   {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)", Login: "default login", Owner: "1"},
		"fable store fable":              {Moves: []string{"default"}, Error: "", Login: "Fable store", Owner: "unknown"},
		"fable store opus holding fable": {Moves: []string{"default"}, Error: "", Login: "Fable store", Owner: "unknown"},
		"mythos 7":                       {Moves: []string{"default"}, Error: "", Login: "Mythos login", Owner: "7"},
		"mythos 8 refreshing":            {Moves: []string(nil), Error: "The session login is refreshing.", Login: "Mythos login", Owner: "8"},
		"mythos owner unknown":           {Moves: []string{"default"}, Error: "", Login: "Mythos login", Owner: "unknown"},
	}
	noMythosNoDefaultAnswers = map[string]sessionAnswer{
		"default fable":                  {Moves: []string{"fable"}, Error: "", Login: "default login", Owner: "unknown"},
		"default mythos holding mythos":  {Moves: []string{"fable"}, Error: "", Login: "default login", Owner: "unknown"},
		"default opus holding fable":     {Moves: []string{"fable"}, Error: "", Login: "default login", Owner: "unknown"},
		"default opus":                   {Moves: []string{"fable"}, Error: "", Login: "default login", Owner: "unknown"},
		"fable store fable":              {Moves: []string{}, Error: "no available Mythos login has quota for mythos\ncannot verify the current default login for the session", Login: "Fable store", Owner: "6"},
		"fable store opus holding fable": {Moves: []string{}, Error: "no available Mythos login has quota for mythos\ncannot verify the current default login for the session", Login: "Fable store", Owner: "6"},
		"mythos 7":                       {Moves: []string{"fable"}, Error: "", Login: "Mythos login", Owner: "7"},
		"mythos 8 refreshing":            {Moves: []string(nil), Error: "The session login is refreshing.", Login: "Mythos login", Owner: "8"},
		"mythos owner unknown":           {Moves: []string{"fable"}, Error: "", Login: "Mythos login", Owner: "unknown"},
	}
	everyLoginRefusedAnswers = map[string]sessionAnswer{
		"default fable":                  {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "default login", Owner: "1"},
		"default mythos holding mythos":  {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "default login", Owner: "1"},
		"default opus holding fable":     {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "default login", Owner: "1"},
		"default opus":                   {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "default login", Owner: "1"},
		"fable store fable":              {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "Fable store", Owner: "unknown"},
		"fable store opus holding fable": {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "Fable store", Owner: "unknown"},
		"mythos 7":                       {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "Mythos login", Owner: "7"},
		"mythos 8 refreshing":            {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "Mythos login", Owner: "8"},
		"mythos owner unknown":           {Moves: []string{}, Error: "no available Mythos login has quota for mythos\nthe Fable store has no usable login (the store is empty)\nAccount-1 is disabled", Login: "Mythos login", Owner: "unknown"},
	}
)

// A tick's Fable placement decides each linked Fable session inside its own
// move guard on what it reads there (DESIGN A117 leaves it per session): one
// Fable start snapshot per session decided. The Fable store's live account is
// at its Fable limit and another account it may take has room, so every
// session stays.
func TestFablePlacementDecidesEachSessionOnItsOwnReads(t *testing.T) {
	for _, n := range []int{1, 4, 12} {
		t.Run(fmt.Sprintf("%d sessions", n), func(t *testing.T) {
			sw, _ := fableStartLogin(t, atFableLimit, "1", "2")
			var sessions []string
			for i := range n {
				sessions = append(sessions, bindFableSession(t, sw, costSessionID(i), fableStoreDir()))
			}
			counts := ownertest.CountReads(t)
			snaps := countFableStartSnapshots(t, nil)
			placeFableSessions(sw)
			identity, pids, roster := counts.Totals()
			mostIdentity, mostPIDs := counts.Most()
			t.Logf("%d Fable sessions: %d identity reads (at most %d of one file), %d live-PID scans (at most %d of one profile), %d roster reads, %d Fable start snapshots",
				n, identity, mostIdentity, pids, mostPIDs, roster, snaps.Load())
			if got := snaps.Load(); got != int32(n) {
				t.Fatalf("a pass over %d Fable sessions took %d Fable start snapshots, want one per session", n, got)
			}
			for _, link := range sessions {
				requireLinkTarget(t, link, fableStoreDir())
			}
		})
	}
}

// One pass, two Fable sessions on the Fable store, whose live account is at
// its Fable limit while the default login's has room: the first decided
// moves to the default login. When the Fable store is switched to an account
// with room as the second is decided, that one stays (DESIGN A117).
func TestFablePlacementPassSeesAFableStoreSwitchBetweenSessions(t *testing.T) {
	for _, switched := range []bool{false, true} {
		t.Run(fmt.Sprintf("switched %v", switched), func(t *testing.T) {
			// Account 3 has a Fable login and room but is allocated to Opus,
			// so the Fable tick would not switch to it: no room until it is live.
			sw, _ := fableStartLogin(t, withUsage(poolAtFableLimit, "3", [3]float64{10, 20, 10}), "1", "2")
			setRecord(t, sw, "3", "models", []any{"opus"})
			first := bindFableSession(t, sw, tickSessionA, fableStoreDir())
			second := bindFableSession(t, sw, tickSessionB, fableStoreDir())
			countFableStartSnapshots(t, func(n int32) {
				if switched && n == 2 {
					writeFile(t, filepath.Join(fableStoreDir(), ".credentials.json"), fableLoginOf("3", farExpiry))
				}
			})
			placeFableSessions(sw)
			var onDefault, onFable int
			for _, link := range []string{first, second} {
				target, err := os.Readlink(link)
				if err != nil {
					t.Fatal(err)
				}
				switch target {
				case sw.DefaultProfileDir():
					onDefault++
				case fableStoreDir():
					onFable++
				}
			}
			if want := map[bool][2]int{false: {2, 0}, true: {1, 1}}[switched]; [2]int{onDefault, onFable} != want {
				t.Fatalf("%d sessions moved to the default login and %d stayed on the Fable store, want %v", onDefault, onFable, want)
			}
		})
	}
}

// BenchmarkStateBuild is one whole dashboard state build on 8 accounts, four
// 250 KiB identity files and 12 linked sessions (DESIGN A117).
func BenchmarkStateBuild(b *testing.B) {
	sw, f := costFixture(b, 250)
	sessions := linkSessions(b, sw, f, 12)
	srv := costServer(b, sw, listedSessions(sessions, sw.DefaultProfileDir()), noTitle)
	srv.Snapshot()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		srv.Snapshot()
	}
}
