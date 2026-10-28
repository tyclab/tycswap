package modelrouting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/wincred"
)

type candidate struct {
	number  string
	fable   bool
	percent float64
}

type request struct {
	Session string `json:"session"`
	Main    string `json:"main"`
	Model   string `json:"model"`
	Agent   string `json:"agent,omitempty"`
}

type lease struct {
	ID      string `json:"id"`
	Account string `json:"account"`
}

type observation struct {
	Model   string
	Account string
	Leased  bool
}

type probe struct {
	mu         sync.Mutex
	store      *store.Store
	candidates []candidate
	leases     map[string]lease
	sequence   int
	observed   []observation
	profile    string
	server     *httptest.Server
	arrived    chan<- struct{}
	proceed    <-chan struct{}
}

func family(model string) string {
	model = strings.TrimSuffix(strings.ToLower(model), "[1m]")
	for _, name := range []string{"fable", "opus", "sonnet", "haiku"} {
		if model == name || strings.HasPrefix(model, "claude-"+name+"-") {
			return name
		}
	}
	return ""
}

func newProbe(t *testing.T) *probe {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the native routing probe uses the Linux file credential backend")
	}
	root := t.TempDir()
	profile := filepath.Join(root, "profile")
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
		t.Setenv(name, profile)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.New(store.Options{OAuth: &oauth.FakeClient{}, Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	active := 1
	data := &store.SequenceData{ActiveAccountNumber: &active, Sequence: []int{1, 2, 3}, Accounts: map[string]json.RawMessage{}}
	for _, number := range []string{"1", "2", "3"} {
		email := "account-" + number + "@example.invalid"
		data.Accounts[number], _ = json.Marshal(map[string]any{"email": email, "organizationUuid": ""})
		plan := "max"
		if number == "3" {
			plan = "pro"
		}
		creds, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "routing-probe-" + number, "refreshToken": "routing-probe-refresh-" + number,
			"expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": plan,
			"scopes": []string{"user:inference", "user:profile"}, "rateLimitTier": "default_claude_max_5x",
		}})
		config, _ := json.Marshal(map[string]any{"hasCompletedOnboarding": true, "oauthAccount": map[string]any{
			"emailAddress": email, "organizationUuid": "", "accountUuid": "00000000-0000-4000-8000-00000000000" + number,
			"hasExtraUsageEnabled": false,
		}})
		if err := s.WriteAccountCredentials(number, email, string(creds)); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteAccountConfig(number, email, string(config)); err != nil {
			t.Fatal(err)
		}
		if number == "1" {
			if err := s.Creds.WriteActive(string(creds)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(profile, ".claude.json"), config, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
	p := &probe{store: s, profile: profile, leases: map[string]lease{}, candidates: []candidate{{"2", true, 20}, {"3", false, 10}, {"1", true, 95}}}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func profileProbe(t *testing.T, base *probe, id groups.ID, account string) *probe {
	t.Helper()
	s, err := base.store.ForGroup(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := switching.SwitchTo(s, account, true, false); err != nil {
		t.Fatal(err)
	}
	p := &probe{store: s, profile: s.ProfileDir(), leases: map[string]lease{}, candidates: base.candidates}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *probe) acquire(r request) (lease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	main, model := family(r.Main), family(r.Model)
	if r.Session == "" || main == "" || model == "" {
		return lease{}, fmt.Errorf("known session and models are required")
	}
	if r.Agent != "" && model == "fable" && main != "fable" {
		return lease{}, fmt.Errorf("a Fable child requires a Fable main session")
	}
	wanted := model
	if r.Agent != "" {
		wanted = main
	}
	var chosen *candidate
	for i := range p.candidates {
		c := &p.candidates[i]
		if c.percent >= 90 || (wanted == "fable" && !c.fable) {
			continue
		}
		if chosen == nil || (wanted != "fable" && chosen.fable && !c.fable) || (chosen.fable == c.fable && c.percent < chosen.percent) {
			chosen = c
		}
	}
	if chosen == nil {
		return lease{}, fmt.Errorf("no account has usable quota for this model")
	}
	for _, held := range p.leases {
		if held.Account != chosen.number {
			return lease{}, fmt.Errorf("another account has an unfinished request")
		}
	}
	if _, err := switching.SwitchTo(p.store, chosen.number, true, false); err != nil {
		return lease{}, err
	}
	p.sequence++
	l := lease{ID: fmt.Sprintf("lease-%d", p.sequence), Account: chosen.number}
	p.leases[l.ID] = l
	return l, nil
}

func (p *probe) release(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.leases, id)
}

func (p *probe) dispatch(model string) error {
	credentials, _, err := p.store.Creds.ReadActive()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"model": model})
	req, err := http.NewRequest(http.MethodPost, p.server.URL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+oauth.ExtractAccessToken(credentials))
	reply, err := p.server.Client().Do(req)
	if err != nil {
		return err
	}
	defer reply.Body.Close()
	_, err = io.Copy(io.Discard, reply.Body)
	return err
}

func (p *probe) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/acquire":
		var input request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		l, err := p.acquire(input)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(l)
	case "/release":
		var l lease
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&l); err != nil {
			http.Error(w, "invalid lease", http.StatusBadRequest)
			return
		}
		p.release(l.ID)
		_, _ = io.WriteString(w, "{}")
	case "/v1/messages/count_tokens":
		_, _ = io.WriteString(w, `{"input_tokens":1}`)
	case "/v1/messages":
		p.messages(w, r)
	default:
		http.Error(w, "unsupported probe endpoint", http.StatusNotFound)
	}
}

func (p *probe) messages(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&input); err != nil {
		http.Error(w, "invalid model request", http.StatusBadRequest)
		return
	}
	account := "unknown"
	for _, number := range []string{"1", "2", "3"} {
		if r.Header.Get("Authorization") == "Bearer routing-probe-"+number {
			account = number
		}
	}
	p.mu.Lock()
	leased := false
	for _, l := range p.leases {
		leased = leased || l.Account == account
	}
	p.observed = append(p.observed, observation{Model: input.Model, Account: account, Leased: leased})
	arrived, proceed := p.arrived, p.proceed
	p.mu.Unlock()
	if arrived != nil {
		arrived <- struct{}{}
		select {
		case <-proceed:
		case <-r.Context().Done():
			return
		}
	}
	message := map[string]any{"id": "msg_probe", "type": "message", "role": "assistant", "model": input.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}
	if !input.Stream {
		message["content"] = []any{map[string]any{"type": "text", "text": "probe ok"}}
		message["stop_reason"] = "end_turn"
		_ = json.NewEncoder(w).Encode(message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []map[string]any{
		{"type": "message_start", "message": message},
		{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "probe ok"}},
		{"type": "content_block_stop", "index": 0},
		{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}},
		{"type": "message_stop"},
	} {
		body, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], body)
	}
}

func TestRouteSequence(t *testing.T) {
	p := newProbe(t)
	for _, step := range []struct{ model, account string }{{"fable", "2"}, {"opus", "3"}, {"fable", "2"}} {
		l, err := p.acquire(request{Session: "same-session", Main: step.model, Model: step.model})
		if err != nil {
			t.Fatal(err)
		}
		got := p.store.CurrentAccountNumber()
		if got == nil || *got != step.account {
			t.Fatalf("%s did not select account %s", step.model, step.account)
		}
		p.release(l.ID)
	}
}

func TestConflictingRequestsHoldTheAccount(t *testing.T) {
	p := newProbe(t)
	first, err := p.acquire(request{Session: "one", Main: "fable", Model: "fable"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.acquire(request{Session: "two", Main: "fable", Model: "fable"})
	if err != nil {
		t.Fatalf("requests for the same account could not overlap: %v", err)
	}
	if _, err := p.acquire(request{Session: "three", Main: "opus", Model: "opus"}); err == nil {
		t.Fatal("a conflicting request changed an in-flight account")
	}
	p.release(first.ID)
	if _, err := p.acquire(request{Session: "three", Main: "opus", Model: "opus"}); err == nil {
		t.Fatal("releasing one request released another request's hold")
	}
	p.release(second.ID)
	if _, err := p.acquire(request{Session: "three", Main: "opus", Model: "opus"}); err != nil {
		t.Fatal(err)
	}
}

func TestGuardFailuresKeepTheCurrentAccount(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		request request
		exhaust bool
	}{
		{"unknown model", request{Session: "one", Main: "custom", Model: "custom"}, false},
		{"Fable child under Opus", request{Session: "one", Main: "opus", Model: "fable", Agent: "child"}, false},
		{"quota unavailable", request{Session: "one", Main: "fable", Model: "fable"}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := newProbe(t)
			if scenario.exhaust {
				p.candidates = nil
			}
			before, _, _ := p.store.Creds.ReadActive()
			if _, err := p.acquire(scenario.request); err == nil {
				t.Fatal("unsafe routing request was accepted")
			}
			after, _, _ := p.store.Creds.ReadActive()
			if before != after {
				t.Fatal("refused routing request changed credentials")
			}
		})
	}
}

func TestOpusChildKeepsTheFableAccount(t *testing.T) {
	p := newProbe(t)
	l, err := p.acquire(request{Session: "one", Main: "fable", Model: "opus", Agent: "child"})
	if err != nil || l.Account != "2" {
		t.Fatalf("child route: %+v, %v", l, err)
	}
}

func TestNaiveSwitchingCanDispatchWithTheWrongAccount(t *testing.T) {
	p := newProbe(t)
	if _, err := switching.SwitchTo(p.store, "2", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := switching.SwitchTo(p.store, "3", true, false); err != nil {
		t.Fatal(err)
	}
	if err := p.dispatch("fable"); err != nil {
		t.Fatal(err)
	}
	if len(p.observed) != 1 || p.observed[0].Account != "3" || p.observed[0].Leased {
		t.Fatalf("the overlapping-session scenario did not expose the dispatch race: %+v", p.observed)
	}
	t.Log("A Fable request selected account 2; a second session selected account 3 before dispatch. Account selection alone does not reserve dispatch.")
}

func TestConcurrentDispatchUsesTheReservedAccount(t *testing.T) {
	p := newProbe(t)
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := range 24 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			model := "fable"
			if i%2 == 1 {
				model = "opus"
			}
			l, err := p.acquire(request{Session: fmt.Sprintf("session-%d", i), Main: model, Model: model})
			if err != nil {
				if !strings.Contains(err.Error(), "unfinished request") {
					t.Error(err)
				}
				return
			}
			defer p.release(l.ID)
			if err := p.dispatch(model); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	workers.Wait()
	if len(p.observed) == 0 {
		t.Fatal("no requests reached the fake provider")
	}
	for _, got := range p.observed {
		wanted := "3"
		if got.Model == "fable" {
			wanted = "2"
		}
		if got.Account != wanted || !got.Leased {
			t.Fatalf("request escaped its account reservation: %+v", got)
		}
	}
	t.Logf("%d requests completed; %d conflicting requests were refused before dispatch", len(p.observed), 24-len(p.observed))
}
