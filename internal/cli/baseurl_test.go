// `add-token --base-url` end to end (DESIGN A46): the flag's grammar, the
// account it stores, how list and status show it, and the switch prompt that
// names the endpoint.
package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/wincred"
)

func TestBaseURLFlagGrammar(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		msg  string
	}{
		{"without add-token", []string{"list", "--base-url", "https://gw.example.com"}, "--base-url can only be used with 'add-token'"},
		{"empty", []string{"add-token", "k", "--base-url", ""}, "argument --base-url: expected a URL"},
		{"missing value", []string{"add-token", "k", "--base-url"}, "argument --base-url: expected one argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errStr := runCLI(t, tc.argv, false, false)
			if code != 2 || !strings.Contains(errStr, tc.msg) {
				t.Fatalf("exit %d, stderr %q; want 2 with %q", code, errStr, tc.msg)
			}
		})
	}
}

// runHuman is runCLI with the process stdout captured too: the human output
// of add-token, list, status and the switch prompt goes there, not to the
// controller's streams.
func runHuman(t *testing.T, argv ...string) (code int, out, errStr string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev, prevLife := os.Stdout, lifecycle.Output
	os.Stdout, lifecycle.Output = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	var streamOut string
	func() {
		defer func() { os.Stdout, lifecycle.Output = prev, prevLife }()
		code, streamOut, errStr = runCLI(t, argv, false, false)
	}()
	w.Close()
	return code, streamOut + <-done, errStr
}

// fakeSwitcher builds every switcher of the test over fakes (no Keychain, no
// network) in the clean home.
func fakeSwitcher(t *testing.T) {
	t.Helper()
	cleanHome(t)
	prev := newSwitcher
	newSwitcher = func(opts store.Options) (*core.Switcher, error) {
		opts.Keychain, opts.OAuth, opts.WinCred, opts.Stderr = keychain.NewFake(), &oauth.FakeClient{}, wincred.NewFake(), io.Discard
		return core.New(opts)
	}
	t.Cleanup(func() { newSwitcher = prev })
}

func TestAddTokenBaseURLEndToEnd(t *testing.T) {
	fakeSwitcher(t)
	withRunningSessions(t, 0)
	withStdinTerminal(t, false)
	const url = "https://gw.example.com/anthropic"

	code, _, errStr := runCLI(t, []string{"add-token", "sk-gw-0123456789", "--base-url", "https://u:p@gw.example.com"}, false, false)
	if code != 1 || !strings.Contains(errStr, "Invalid --base-url") {
		t.Fatalf("invalid URL: exit %d, stderr %q", code, errStr)
	}

	code, out, errStr := runHuman(t, "add-token", "sk-gw-0123456789", "--base-url", url, "--email", "gw@example.com")
	if code != 0 {
		t.Fatalf("add-token --base-url: exit %d, stderr %q", code, errStr)
	}
	if !strings.Contains(out, "(from API key for gw.example.com)") {
		t.Errorf("add-token output = %q", out)
	}
	if code, _, errStr := runCLI(t, []string{"add-token", "sk-ant-oat01-other", "--email", "a@example.com"}, false, false); code != 0 {
		t.Fatalf("second account: exit %d, %q", code, errStr)
	}

	code, out, _ = runCLI(t, []string{"list", "--json"}, false, false)
	if code != 0 {
		t.Fatalf("list --json: exit %d", code)
	}
	var list struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("list --json is not JSON: %q", out)
	}
	if len(list.Accounts) != 2 || list.Accounts[0]["baseUrl"] != url || list.Accounts[0]["usageStatus"] != "api_key" {
		t.Errorf("list --json rows = %v", list.Accounts)
	}
	if _, present := list.Accounts[1]["baseUrl"]; present {
		t.Errorf("the setup-token row carries a baseUrl: %v", list.Accounts[1])
	}
	if _, out, _ = runHuman(t, "list"); !strings.Contains(out, "→ gw.example.com") || strings.Contains(out, url) {
		t.Errorf("human list shows not just the host:\n%s", out)
	}

	// The switch asks, and the question names the whole URL.
	code, out, errStr = runHuman(t, "switch", "1")
	if code != 1 || !strings.Contains(errStr, "Not a terminal") || !strings.Contains(out, "sends Claude Code's requests to "+url) {
		t.Fatalf("switch 1 without a terminal: exit %d, stdout %q, stderr %q", code, out, errStr)
	}
	if code, out, errStr = runHuman(t, "switch", "1", "--yes"); code != 0 {
		t.Fatalf("switch 1 --yes: exit %d, stdout %q, stderr %q", code, out, errStr)
	}
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("settings.json: %v\n%s", err, raw)
	}
	if settings["env"]["ANTHROPIC_BASE_URL"] != url || settings["env"]["ANTHROPIC_AUTH_TOKEN"] != "sk-gw-0123456789" {
		t.Errorf("settings.json env = %v", settings["env"])
	}

	if _, out, _ = runHuman(t, "status"); !strings.Contains(out, "Endpoint: gw.example.com") {
		t.Errorf("status:\n%s", out)
	}
	_, out, _ = runCLI(t, []string{"status", "--json"}, false, false)
	var status struct {
		Active map[string]any `json:"active"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil || status.Active["baseUrl"] != url || status.Active["usageStatus"] != "api_key" {
		t.Errorf("status --json = %q (%v)", out, err)
	}

	// And back: the subscription account, and settings.json as it was (none).
	if code, out, errStr = runHuman(t, "switch", "2"); code != 0 {
		t.Fatalf("switch 2: exit %d, stdout %q, stderr %q", code, out, errStr)
	}
	raw, _ = os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.TrimSpace(string(raw)) != "{}" {
		t.Errorf("settings.json after the switch back = %q, want the empty object a missing file read as", raw)
	}
}
