package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/reporting"
)

type fakeRecovery struct {
	mu      sync.Mutex
	starts  int
	request RecoveryStartRequest
}

func (f *fakeRecovery) View(reporting.AccountsSnapshot) RecoveryView {
	return RecoveryView{Incidents: []recovery.Incident{}, WaitMinutes: 30, SessionModels: map[string]string{"s": "opus"}}
}
func (f *fakeRecovery) Prepare(id string, src recovery.Source) (recovery.Packet, error) {
	src.SessionID = id
	src.Provider = "claude"
	src.CWD = "/tmp/work"
	return recovery.Prepare(src)
}
func (f *fakeRecovery) Dismiss(id, incident string) error {
	if id == "" || incident == "" {
		return cerr.Validation("session and incident required")
	}
	return nil
}
func (f *fakeRecovery) Start(request RecoveryStartRequest) (RecoveryStartResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.request = request
	if !request.Explicit || !request.PendingConfirmed {
		return RecoveryStartResult{}, cerr.Validation("explicit reviewed ownership transfer required")
	}
	f.starts++
	return RecoveryStartResult{Started: true, Message: "Fixture terminal started"}, nil
}

func TestRecoveryRoutesPreserveExplicitReviewAndNilSurface(t *testing.T) {
	f := &fakeRecovery{}
	server, err := New(Deps{Facade: &fakeFacade{snap: sampleSnapshot()}, Recovery: f, Sessions: func() SessionsView { return SessionsView{} }, AuthOverrides: func() AuthOverridesView { return AuthOverridesView{} }})
	if err != nil {
		t.Fatal(err)
	}
	post := func(s *Server, path string, value any) *http.Response {
		data, _ := json.Marshal(value)
		request := httptest.NewRequest("POST", path, bytes.NewReader(data))
		recorder := httptest.NewRecorder()
		switch path {
		case "/api/recovery/prepare":
			s.handleRecoveryPrepare(recorder, request)
		case "/api/recovery/start":
			s.handleRecoveryStart(recorder, request)
		}
		return recorder.Result()
	}
	response := post(server, "/api/recovery/prepare", map[string]any{"sessionId": "s", "context": map[string]any{"objective": "Continue saved work"}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", response.StatusCode, readBody(t, response))
	}
	result := decodeJSON(t, response)["result"].(map[string]any)
	if result["packet"].(map[string]any)["digest"] == "" {
		t.Fatal("packet has no review digest")
	}
	response = post(server, "/api/recovery/start", RecoveryStartRequest{Explicit: false, PendingConfirmed: true})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("%d", response.StatusCode)
	}
	response.Body.Close()
	response = post(server, "/api/recovery/start", RecoveryStartRequest{Explicit: true, PendingConfirmed: true, ReviewedDigest: "exact"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", response.StatusCode, readBody(t, response))
	}
	response.Body.Close()
	f.mu.Lock()
	if f.starts != 1 || f.request.ReviewedDigest != "exact" {
		t.Fatalf("start count %d request %#v", f.starts, f.request)
	}
	f.mu.Unlock()
	absent, err := New(Deps{Facade: &fakeFacade{snap: sampleSnapshot()}})
	if err != nil {
		t.Fatal(err)
	}
	response = post(absent, "/api/recovery/start", RecoveryStartRequest{Explicit: true})
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil recovery %d", response.StatusCode)
	}
	response.Body.Close()
}
