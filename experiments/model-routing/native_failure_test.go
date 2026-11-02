package modelrouting

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeWorkerCrashBypassesModGuard(t *testing.T) {
	if os.Getenv("TYCSWAP_ROUTING_NATIVE_TEST") != "1" {
		t.Skip("set TYCSWAP_ROUTING_NATIVE_TEST=1 for the native worker failure probe")
	}
	p := newProbe(t)
	plugin := t.TempDir()
	for _, name := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "hooks/register.js"} {
		body, err := os.ReadFile(filepath.Join("plugin", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "hooks/register.js" {
			body = []byte(strings.Replace(string(body), "const lease = await call", "while (true) {}\n    const lease = await call", 1))
		}
		path := filepath.Join(plugin, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	cmd := nativeCommand(t, p, ctx, "fable")
	for i, arg := range cmd.Args {
		if arg == "--plugin-dir" {
			cmd.Args[i+1] = plugin
		}
	}
	debugPath := filepath.Join(t.TempDir(), "native.log")
	cmd.Args = append(cmd.Args, "--debug-file", debugPath, "Reply with probe ok.")
	output, err := cmd.CombinedOutput()
	debug, _ := os.ReadFile(debugPath)
	var failureLog []string
	for _, line := range strings.Split(string(debug), "\n") {
		if strings.Contains(line, "worker") || strings.Contains(line, "hook skipped") || strings.Contains(line, "not loaded") || strings.Contains(line, "unloaded") {
			failureLog = append(failureLog, line)
		}
	}
	p.mu.Lock()
	observed := append([]observation(nil), p.observed...)
	p.mu.Unlock()
	t.Logf("native result=%v output=%.1800s observations=%+v failure log=%.4000s", err, output, observed, strings.Join(failureLog, "\n"))
	if len(observed) == 0 {
		t.Fatal("expected a characterization of the native worker fail-open path; no model request observed")
	}
	for _, got := range observed {
		if got.Leased {
			t.Fatal("crashed worker unexpectedly acquired a routing lease")
		}
	}
	if !strings.Contains(string(debug), "crashed") && !strings.Contains(string(debug), "unloaded") {
		t.Fatal("native output did not confirm worker failure")
	}
}
