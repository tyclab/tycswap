package modelrouting

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
)

func nativeCommand(t *testing.T, p *probe, ctx context.Context, model string) *exec.Cmd {
	t.Helper()
	if os.Getenv("TYCSWAP_ROUTING_NATIVE_TEST") != "1" {
		t.Skip("set TYCSWAP_ROUTING_NATIVE_TEST=1 to run Claude against the local fake API")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := filepath.Abs("plugin")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, claude, "-p", "--model", model, "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "user", "--plugin-dir", plugin, "--no-session-persistence")
	cmd.Dir = t.TempDir()
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_CODE_OAUTH") || strings.HasPrefix(key, "CLAUDE_CODE_API_KEY") || strings.HasPrefix(key, "CLAUDE_CODE_USE_") || key == "TYCSWAP_ROUTING_PROBE_URL" || key == "CLAUDE_CONFIG_DIR" || key == "CLAUDE_SECURESTORAGE_CONFIG_DIR" {
			continue
		}
		cmd.Env = append(cmd.Env, variable)
	}
	cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+p.profile, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+p.profile, "ANTHROPIC_BASE_URL="+p.server.URL, "TYCSWAP_ROUTING_PROBE_URL="+p.server.URL, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1")
	return cmd
}

func TestNativeClaudeRoutesOneRunningProcess(t *testing.T) {
	p := newProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := nativeCommand(t, p, ctx, "fable")
	cmd.Args = append(cmd.Args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
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
	messages := make(chan map[string]any, 64)
	finished := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			var message map[string]any
			if json.Unmarshal(scanner.Bytes(), &message) == nil {
				messages <- message
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
			t.Logf("Claude stderr: %.1500s", stderr.String())
		}
	})
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
			case message, ok := <-messages:
				if !ok {
					t.Fatal("Claude exited before the expected response")
				}
				if message["type"] == kind {
					return message
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %s: %v", kind, ctx.Err())
			}
		}
	}
	sessionID := ""
	candidates := append([]candidate(nil), p.candidates...)
	for index, model := range []string{"fable", "opus", "fable", "fable", "opus"} {
		p.mu.Lock()
		p.candidates = candidates
		if index == 3 {
			p.candidates = nil
		}
		p.mu.Unlock()
		if index > 0 {
			write(map[string]any{"type": "control_request", "request_id": fmt.Sprintf("model-%d", index), "request": map[string]any{"subtype": "set_model", "model": model}})
			response := wait("control_response")
			data, _ := response["response"].(map[string]any)
			if data["subtype"] != "success" {
				t.Fatalf("model change failed: %+v", data)
			}
		}
		write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "Reply with probe ok."}})
		result := wait("result")
		if result["is_error"] == true && index != 3 {
			t.Fatalf("Claude reported a failed turn: %.1000v", result)
		}
		id, _ := result["session_id"].(string)
		if id == "" || sessionID != "" && id != sessionID {
			t.Fatal("model routing changed the native session identity")
		}
		sessionID = id
	}
	p.mu.Lock()
	observed := append([]observation(nil), p.observed...)
	p.mu.Unlock()
	wanted := []struct{ family, account string }{{"fable", "2"}, {"opus", "3"}, {"fable", "2"}, {"opus", "3"}}
	if len(observed) != len(wanted) {
		t.Fatalf("expected four model requests and one blocked turn, observed %+v", observed)
	}
	for i, got := range observed {
		if family(got.Model) != wanted[i].family || got.Account != wanted[i].account || !got.Leased {
			t.Errorf("request %d: model=%s account=%s leased=%t; wanted %s account %s", i, got.Model, got.Account, got.Leased, wanted[i].family, wanted[i].account)
		}
	}
	t.Log("One Claude process and session sent Fable/Opus/Fable/Opus with accounts 2/3/2/3; a turn with no usable account sent no request.")
}

func TestNativeClaudeProfilesRunDifferentAccountsInParallel(t *testing.T) {
	base := newProbe(t)
	fable := profileProbe(t, base, groups.Fable, "2")
	opus := profileProbe(t, base, groups.Opus, "3")
	arrived := make(chan struct{}, 2)
	proceed := make(chan struct{})
	for _, p := range []*probe{fable, opus} {
		p.mu.Lock()
		p.arrived, p.proceed = arrived, proceed
		p.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	completed := make(chan error, 2)
	for _, item := range []struct {
		p     *probe
		model string
	}{{fable, "fable"}, {opus, "opus"}} {
		cmd := nativeCommand(t, item.p, ctx, item.model)
		cmd.Args = append(cmd.Args, "Reply with probe ok.")
		go func() {
			output, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("Claude process: %w; %.800s", err, output)
			}
			completed <- err
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case err := <-completed:
			cancel()
			t.Fatalf("a process ended before both requests overlapped: %v", err)
		case <-ctx.Done():
			t.Fatal("the two profiles did not dispatch concurrently")
		}
	}
	close(proceed)
	for range 2 {
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		p       *probe
		account string
	}{{fable, "2"}, {opus, "3"}} {
		item.p.mu.Lock()
		observed := append([]observation(nil), item.p.observed...)
		item.p.mu.Unlock()
		if len(observed) != 1 || observed[0].Account != item.account || !observed[0].Leased {
			t.Fatalf("profile used the wrong account: %+v", observed)
		}
	}
	t.Log("Fable and Opus requests overlapped in separate Claude processes, using accounts 2 and 3 respectively.")
}
