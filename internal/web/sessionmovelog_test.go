package web

import "testing"

func TestDashboardSessionMoveLeavesLoggingToTheMoveGuard(t *testing.T) {
	s, _ := fixtureControlsServer(t)
	var actions []string
	s.d.ActionLog = func(change, actor string) { actions = append(actions, change+" by "+actor) }
	res := fixtureRequest(s, "POST", "/api/sessions/"+fixtureSession+"/use", `{"store":"default"}`)
	if res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	if len(actions) != 0 {
		t.Fatalf("route wrote %q on top of the guarded move outcome", actions)
	}
}
