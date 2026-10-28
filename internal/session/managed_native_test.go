package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/routing"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/usage"
)

func runNativeRoutingHelper() int {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "groups" || args[0] == "recovery") {
		return 0
	}
	if len(args) != 4 || args[0] != "routing" || args[2] != "--group" {
		return 2
	}
	id, err := groups.Parse(args[3])
	if err != nil {
		return 2
	}
	s, err := store.New(store.Options{OAuth: &oauth.FakeClient{}, Keychain: keychain.NewFake(), Stderr: io.Discard})
	if err != nil {
		return 1
	}
	s, err = s.ForGroup(id)
	if err != nil {
		return 1
	}
	var request routing.Request
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		return 2
	}
	coordinator := routing.Coordinator{Store: s}
	var result routing.Result
	switch args[1] {
	case "register":
		result, err = coordinator.Register(request)
	case "acquire":
		result, err = coordinator.Acquire(request)
	case "release":
		result, err = coordinator.Release(request)
	case "unregister":
		result, err = coordinator.Unregister(request)
	default:
		return 2
	}
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
		return 1
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	return 0
}

func TestNativeManagedRoutingUsesProductionCoordinator(t *testing.T) {
	if os.Getenv("TYCSWAP_ROUTING_NATIVE_TEST") != "1" || runtime.GOOS != "linux" {
		t.Skip("set TYCSWAP_ROUTING_NATIVE_TEST=1 on Linux for native routing")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	testutil.IsolateHome(t)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_CODE_OAUTH") || strings.HasPrefix(name, "CLAUDE_CODE_USE_") || name == "CLAUDE_CONFIG_DIR" || name == "CLAUDE_SECURESTORAGE_CONFIG_DIR" {
			t.Setenv(name, "")
		}
	}
	t.Setenv(childRoleEnv, "routing-native")
	sw, err := core.New(store.Options{OAuth: &oauth.FakeClient{}, Keychain: keychain.NewFake(), Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	roster := &store.SequenceData{Sequence: []int{1, 2, 3}, Accounts: map[string]json.RawMessage{}}
	for _, num := range []string{"1", "2", "3"} {
		email := "native-" + num + "@example.invalid"
		plan := "max"
		if num == "3" {
			plan = "pro"
		}
		roster.Accounts[num], _ = json.Marshal(map[string]any{"email": email, "organizationUuid": "", "subscriptionType": plan})
		creds, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "native-route-" + num, "refreshToken": "native-refresh-" + num,
			"expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": plan,
			"scopes": []string{"user:inference", "user:profile"}, "rateLimitTier": "default_claude_max_5x",
		}})
		config, _ := json.Marshal(map[string]any{"hasCompletedOnboarding": true, "oauthAccount": map[string]any{
			"emailAddress": email, "organizationUuid": "", "accountUuid": "00000000-0000-4000-8000-00000000000" + num, "hasExtraUsageEnabled": false,
		}})
		if err := sw.WriteAccountCredentials(num, email, string(creds)); err != nil {
			t.Fatal(err)
		}
		if err := sw.Store.WriteAccountConfig(num, email, string(config)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sw.WriteSequence(roster); err != nil {
		t.Fatal(err)
	}
	setQuota := func(exhausted bool) {
		t.Helper()
		for _, num := range []string{"1", "2", "3"} {
			pct := map[string]float64{"1": 90, "2": 20, "3": 10}[num]
			if exhausted {
				pct = 100
			}
			limits := map[string]any{"five_hour": map[string]any{"pct": pct}, "seven_day": map[string]any{"pct": pct}, "scoped": []any{map[string]any{"name": "Fable", "pct": pct}, map[string]any{"name": "Opus", "pct": pct}}}
			if err := sw.Usage.Record(map[string]usage.FetchRecord{num: {Usage: limits}}, map[string]usage.Identity{num: {Email: "native-" + num + "@example.invalid"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	setQuota(false)
	manager := NewManager(sw, Options{OAuth: sw.OAuth, Keychain: sw.Keychain(), Stdout: io.Discard, HookExecutable: helper})
	launch, err := manager.PrepareManaged("2", "fable", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type observation struct {
		model, account string
		leased         bool
	}
	var mu sync.Mutex
	var observed []observation
	arrived, proceed := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages/count_tokens" {
			_, _ = io.WriteString(w, `{"input_tokens":1}`)
			return
		}
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&input); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer native-route-")
		leased := false
		state, err := routingstate.Read(sw.SharedRoot())
		if err == nil {
			for _, lease := range state.Leases {
				family, _ := routingstate.Family(lease.Model)
				requested, _ := routingstate.Family(input.Model)
				leased = leased || lease.Account == account && lease.Group == "fable" && family == requested && lease.Session != launch.LaunchID
			}
		}
		mu.Lock()
		observed = append(observed, observation{input.Model, account, leased})
		first := len(observed) == 1
		mu.Unlock()
		if first {
			close(arrived)
			select {
			case <-proceed:
			case <-r.Context().Done():
				return
			}
		}
		writeNativeRoutingStream(w, input.Model)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	barrier := filepath.Join(t.TempDir(), "ready")
	script := filepath.Join(t.TempDir(), "start-native")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nwhile [[ ! -f \"$1\" ]]; do sleep 0.01; done\nshift\nexport TYCSWAP_ROUTE_PID=$$\nexec \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	args := append([]string{barrier, claude}, launch.Args...)
	args = append(args, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "user", "--no-session-persistence")
	cmd := exec.CommandContext(ctx, script, args...)
	cmd.Dir = launch.CWD
	cmd.Env = append(launch.Env, "ANTHROPIC_BASE_URL="+server.URL, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+launch.ProfileDir, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	messages, finished := make(chan map[string]any, 64), make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			var value map[string]any
			if json.Unmarshal(scanner.Bytes(), &value) == nil {
				messages <- value
			}
		}
		finished <- cmd.Wait()
		close(messages)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		cancel()
		<-finished
		if t.Failed() {
			t.Logf("native stderr: %.2000s", stderr.String())
		}
	})
	if err := routingstate.With(sw.SharedRoot(), func(state *routingstate.State) error {
		for key, record := range state.Sessions {
			record.PID = cmd.Process.Pid
			state.Sessions[key] = record
		}
		for key, record := range state.Leases {
			record.PID = cmd.Process.Pid
			state.Leases[key] = record
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(barrier, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(value any) {
		t.Helper()
		if err := json.NewEncoder(stdin).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(kind string) map[string]any {
		t.Helper()
		for {
			select {
			case value, ok := <-messages:
				if !ok {
					t.Fatal("native session exited before " + kind)
				}
				if value["type"] == kind {
					return value
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	for index, model := range []string{"fable", "opus", "fable", "fable", "opus"} {
		setQuota(index == 3)
		if index > 0 {
			write(map[string]any{"type": "control_request", "request_id": strconv.Itoa(index), "request": map[string]string{"subtype": "set_model", "model": model}})
			control := wait("control_response")
			response, _ := control["response"].(map[string]any)
			if response["subtype"] != "success" {
				t.Fatalf("model switch failed: %v", response)
			}
		}
		write(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": "Reply with native probe ok."}})
		if index == 0 {
			select {
			case <-arrived:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			scope, _ := sw.Store.ForGroup(groups.Fable)
			if _, err := switching.SwitchTo(scope.WithRoutingModel(groups.Opus), "3", true, false); err == nil {
				t.Fatal("manual switch replaced an active native request")
			}
			close(proceed)
		}
		result := wait("result")
		if result["session_id"] != launch.SessionID {
			t.Fatalf("native session changed: %v", result["session_id"])
		}
		if index == 3 && !strings.Contains(fmt.Sprint(result["result"]), "No model request was sent") {
			t.Fatalf("exhausted quota did not report a blocked request: %v", result)
		}
		state, err := routingstate.Read(sw.SharedRoot())
		if err != nil || len(state.Leases) != 0 {
			t.Fatalf("request was not released: %v %+v", err, state)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"2", "3", "2", "3"}
	if len(observed) != len(want) {
		t.Fatalf("wrong native dispatch count: %+v", observed)
	}
	for i, got := range observed {
		if got.account != want[i] || !got.leased {
			t.Errorf("dispatch %d: %+v, want leased account %s", i, got, want[i])
		}
	}
	t.Log("Production plugin and coordinator kept one native session; Fable/Opus/Fable/Opus used 2/3/2/3, exhausted quota sent nothing, and streamed requests blocked conflicting switches.")
}

func writeNativeRoutingStream(w http.ResponseWriter, model string) {
	message := map[string]any{"id": "msg_native", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []map[string]any{
		{"type": "message_start", "message": message},
		{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "native probe ok"}},
		{"type": "content_block_stop", "index": 0},
		{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}},
		{"type": "message_stop"},
	} {
		body, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], body)
	}
}
