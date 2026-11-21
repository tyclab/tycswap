package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
)

const fixtureSession = "00112233-4455-4677-8899-aabbccddeeff"

type fixtureLoginControls struct {
	*fakeOps
	next              string
	readErr, writeErr error
}

func (o *fixtureLoginControls) SwitchMythos(id string) (map[string]any, error) {
	o.record("switch " + id)
	return nil, o.writeErr
}
func (o *fixtureLoginControls) UseSession(id, store string) error {
	o.record("use " + id + " " + store)
	return o.writeErr
}
func (o *fixtureLoginControls) SessionMoves(id string) ([]string, error) {
	if id == fixtureSession {
		return []string{"mythos", "fable"}, nil
	}
	return nil, errors.New("session has no binding")
}
func (o *fixtureLoginControls) SessionLogin(id string) (string, string) {
	if id == fixtureSession {
		return "Mythos login", "8"
	}
	return "", ""
}

func (o *fixtureLoginControls) NextStartModel() (string, error) { return o.next, o.readErr }
func (o *fixtureLoginControls) SetNextStartModel(model string) error {
	o.record("next " + model)
	return o.writeErr
}
func (o *fixtureLoginControls) SetModels(id string, models []string) error {
	o.record(fmt.Sprintf("models %s %v", id, models))
	return o.writeErr
}

func fixtureControlsServer(t *testing.T) (*Server, *fixtureLoginControls) {
	t.Helper()
	s := sampleSnapshot()
	for _, item := range []struct {
		dest any
		data string
	}{
		{s, `{"mythos":{"number":1,"email":"alice@example.com","countedWindows":["5h","7d","Mythos"],"linkedSessions":2}}`},
		{&s.Accounts[0], `{"mythosLogin":{"live":true,"status":"ok","selectable":true},"models":[]}`},
		{&s.Accounts[1], `{"mythosLogin":{"status":"sign in again","selectable":false},"models":["mythos"]}`},
	} {
		if err := json.Unmarshal([]byte(item.data), item.dest); err != nil {
			t.Fatal(err)
		}
	}
	ops := &fixtureLoginControls{fakeOps: &fakeOps{}, next: "mythos"}
	server, err := New(Deps{Facade: &fakeFacade{snap: s, backupDir: t.TempDir()}, Accounts: ops, RemoteToken: "fixture",
		Sessions: func() SessionsView {
			sessions := sampleSessions()
			sessions.Claude[0].SessionID = fixtureSession
			return sessions
		},
		SessionTitle: func(string, string, string) string { return "" }, AuthOverrides: func() AuthOverridesView { return AuthOverridesView{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, ops
}
func fixtureRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:8123"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer fixture")
	out := httptest.NewRecorder()
	s.handler.ServeHTTP(out, req)
	return out
}

func TestDashboardLinkedLoginState(t *testing.T) {
	s, ops := fixtureControlsServer(t)
	get := func() map[string]any {
		resp := fixtureRequest(s, "GET", "/api/state", "")
		var doc map[string]any
		if resp.Code != 200 || json.Unmarshal(resp.Body.Bytes(), &doc) != nil {
			t.Fatal(resp.Body.String())
		}
		return doc
	}
	doc := get()
	summary, ok := doc["mythos"].(map[string]any)
	if !ok || summary["linkedSessions"] != 2.0 || summary["number"] != 1.0 || !reflect.DeepEqual(summary["countedWindows"], []any{"5h", "7d", "Mythos"}) {
		t.Errorf("selected Mythos login = %v", summary)
	}
	rows := doc["accounts"].([]any)
	if !reflect.DeepEqual(rows[0].(map[string]any)["models"], []any{}) || !reflect.DeepEqual(rows[1].(map[string]any)["models"], []any{"mythos"}) || rows[2].(map[string]any)["models"] != nil {
		t.Error("models must distinguish known derived, allocated, and unavailable")
	}
	if login, ok := rows[1].(map[string]any)["mythosLogin"].(map[string]any); !ok || login["status"] != "sign in again" || login["selectable"] != false {
		t.Errorf("sign-in-needed row = %v", login)
	}
	sessions := doc["sessions"].(map[string]any)["claude"].([]any)
	if !reflect.DeepEqual(sessions[0].(map[string]any)["moves"], []any{"mythos", "fable"}) || sessions[1].(map[string]any)["moves"] != nil {
		t.Error("move offers must come from the session verifier")
	}
	if row := sessions[0].(map[string]any); row["login"] != "Mythos login" || row["accountNumber"] != "8" {
		t.Errorf("linked session identity = (%v,%v)", row["login"], row["accountNumber"])
	}
	if row := sessions[1].(map[string]any); row["login"] != nil || row["accountNumber"] != nil {
		t.Error("unlinked session acquired an owner")
	}
	if doc["nextStartModel"] != "mythos" {
		t.Errorf("next start = %v", doc["nextStartModel"])
	}
	ops.next = ""
	if got := get()["nextStartModel"]; got != "" {
		t.Errorf("cleared next start = %#v", got)
	}
	ops.readErr = errors.New("unreadable fixture")
	doc = get()
	if doc["nextStartModel"] != nil || doc["nextStartError"] != "unreadable fixture" {
		t.Errorf("unreadable selection = (%v, %v)", doc["nextStartModel"], doc["nextStartError"])
	}
}

func TestDashboardLinkedLoginMutations(t *testing.T) {
	session := "/api/sessions/" + fixtureSession + "/use"
	models := "/api/accounts/claude:1/models"
	next := "/api/sessions/next-start"
	sw := "/api/switch/claude:1?store=mythos"
	for _, tc := range []struct {
		name, path, body string
		err              error
		status           int
		call             string
	}{
		{"select", sw, "", nil, 200, "switch 1"}, {"busy login", sw, "", cerr.Lock("fixture busy"), 409, "switch 1"},
		{"missing login", sw, "", cerr.AccountNotFound("fixture gone"), 404, "switch 1"},
		{"codex switch", "/api/switch/codex:1?store=mythos", "", nil, 400, ""}, {"empty switch", sw + "&empty=1", "", nil, 400, ""},
		{"move", session, `{"store":"fable"}`, nil, 200, "use " + fixtureSession + " fable"},
		{"busy move", session, `{"store":"mythos"}`, cerr.Lock("fixture busy"), 409, "use " + fixtureSession + " mythos"},
		{"missing move", session, `{"store":"default"}`, cerr.AccountNotFound("fixture gone"), 404, "use " + fixtureSession + " default"},
		{"missing store", session, `{}`, nil, 400, ""}, {"wrong store", session, `{"store":"opus"}`, nil, 400, ""},
		{"allocate", models, `{"models":["mythos","fable"]}`, nil, 200, "models 1 [mythos fable]"},
		{"clear allocation", models, `{"models":[]}`, nil, 200, "models 1 []"}, {"busy editor", models, `{"models":["opus"]}`, cerr.Lock("fixture busy"), 409, "models 1 [opus]"},
		{"missing allocation", models, `{}`, nil, 400, ""}, {"null allocation", models, `{"models":null}`, nil, 400, ""},
		{"codex allocation", "/api/accounts/codex:1/models", `{"models":[]}`, nil, 400, ""},
		{"queue", next, `{"model":"mythos"}`, nil, 200, "next mythos"}, {"clear queue", next, `{"model":""}`, nil, 200, "next "},
		{"missing queue", next, `{}`, nil, 400, ""}, {"null queue", next, `{"model":null}`, nil, 400, ""},
		{"invalid queue", next, `{"model":"fable"}`, nil, 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ops := fixtureControlsServer(t)
			ops.writeErr = tc.err
			res := fixtureRequest(s, "POST", tc.path, tc.body)
			if res.Code != tc.status {
				t.Errorf("HTTP %d, want %d: %s", res.Code, tc.status, res.Body.String())
			}
			if tc.err != nil && !strings.Contains(res.Body.String(), tc.err.Error()) {
				t.Errorf("missing refusal reason %q", tc.err)
			}
			var want []string
			if tc.call != "" {
				want = []string{tc.call}
			}
			if got := ops.Calls(); !reflect.DeepEqual(got, want) {
				t.Errorf("operations = %v, want %v", got, want)
			}
		})
	}
}

func TestDashboardLinkedLoginPage(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.Command(node, "testdata/fable.cjs", "static/app.js", "mythos").CombinedOutput(); err != nil {
		t.Fatalf("dashboard controls: %v\n%s", err, out)
	}
	guide := staticFile(t, "index.html")
	for _, term := range []string{`id="g-mythos"`, "Use for Mythos", "--store mythos", "Set next start"} {
		if !strings.Contains(guide, term) {
			t.Errorf("guide is missing %s", term)
		}
	}
}

func TestDashboardModelEditorRefusalKeepsInput(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.Command(node, "testdata/fable.cjs", "static/app.js", "editor").CombinedOutput(); err != nil {
		t.Fatalf("model editor: %v\n%s", err, out)
	}
}

func TestDashboardSessionOwnerAndRefusal(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.Command(node, "testdata/fable.cjs", "static/app.js", "session-owner").CombinedOutput(); err != nil {
		t.Fatalf("session rows: %v\n%s", err, out)
	}
}
