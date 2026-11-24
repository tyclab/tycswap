// Tests for the first page's onboarding (DESIGN A27): the live login that is
// not stored yet and the auth-overrides notice.
package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
)

func TestStateCurrentLogin(t *testing.T) {
	h := newHarness(t)
	// The sample snapshot's active account is #1: the live login is stored.
	st := decodeJSON(t, h.get("/api/state"))
	if !reflect.DeepEqual(st["currentLogin"], map[string]any{"email": "alice@example.com", "saved": true}) {
		t.Errorf("currentLogin = %v", st["currentLogin"])
	}
	// No stored account is active: the login is not stored yet.
	h.fa.mu.Lock()
	h.fa.snap = &reporting.AccountsSnapshot{Accounts: sampleSnapshot().Accounts}
	h.fa.mu.Unlock()
	h.setLogin("dana@example.com", true)
	st = decodeJSON(t, h.get("/api/state"))
	if !reflect.DeepEqual(st["currentLogin"], map[string]any{"email": "dana@example.com", "saved": false}) {
		t.Errorf("currentLogin = %v", st["currentLogin"])
	}
	// Claude Code has no login at all.
	h.setLogin("", false)
	st = decodeJSON(t, h.get("/api/state"))
	if v, present := st["currentLogin"]; !present || v != nil {
		t.Errorf("currentLogin = %v, want null", v)
	}
	// A server without the seam says nothing.
	h2 := newHarness(t, withNoCurrentLogin())
	if v := decodeJSON(t, h2.get("/api/state"))["currentLogin"]; v != nil {
		t.Errorf("currentLogin without the seam = %v", v)
	}
}

func TestDetectAuthOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	getenv := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	// Nothing set, no file: nothing, and the lists are [] not null.
	v := DetectAuthOverrides(getenv(nil), path)
	if v.Any() || v.Env == nil || v.Settings == nil || v.SettingsPath != path {
		t.Errorf("empty = %+v", v)
	}
	// The variables, in their order; empty ones select nothing. A
	// setup-token in CLAUDE_CODE_OAUTH_TOKEN bypasses the stored login too
	// (launch.AuthOverrideEnvVars).
	v = DetectAuthOverrides(getenv(map[string]string{"ANTHROPIC_BASE_URL": "https://proxy.example", "ANTHROPIC_API_KEY": "  ", "ANTHROPIC_AUTH_TOKEN": "t", "CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-x"}), path)
	if !reflect.DeepEqual(v.Env, []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"}) || len(v.Settings) != 0 {
		t.Errorf("env = %+v", v)
	}
	// The settings file's env block and apiKeyHelper; blanks and nulls skipped.
	if err := os.WriteFile(path, []byte(`{"apiKeyHelper": "/usr/local/bin/key.sh", "env": {"ANTHROPIC_API_KEY": "sk-x", "ANTHROPIC_AUTH_TOKEN": "", "ANTHROPIC_BASE_URL": null, "CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-y", "OTHER": "1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	v = DetectAuthOverrides(getenv(nil), path)
	if !reflect.DeepEqual(v.Settings, []string{"apiKeyHelper", "env.ANTHROPIC_API_KEY", "env.CLAUDE_CODE_OAUTH_TOKEN"}) || len(v.Env) != 0 {
		t.Errorf("settings = %+v", v)
	}
	// A file that does not parse declares nothing.
	if err := os.WriteFile(path, []byte(`{"env": `), 0o600); err != nil {
		t.Fatal(err)
	}
	if v = DetectAuthOverrides(getenv(nil), path); len(v.Settings) != 0 {
		t.Errorf("corrupt file = %+v", v)
	}
	// A nil getenv reads no environment.
	if v = DetectAuthOverrides(nil, path); len(v.Env) != 0 {
		t.Errorf("nil getenv = %+v", v)
	}
}

// The state carries names only, never a value.
func TestStateAuthOverrides(t *testing.T) {
	h := newHarness(t)
	h.setOverrides(AuthOverridesView{Env: []string{"ANTHROPIC_API_KEY"}, Settings: []string{"env.ANTHROPIC_BASE_URL"}, SettingsPath: "/home/t/.claude/settings.json"})
	raw := readBody(t, h.get("/api/state"))
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"env": []any{"ANTHROPIC_API_KEY"}, "settings": []any{"env.ANTHROPIC_BASE_URL"}, "settingsPath": "/home/t/.claude/settings.json"}
	if !reflect.DeepEqual(st["authOverrides"], want) {
		t.Errorf("authOverrides = %v", st["authOverrides"])
	}
	// Nil slices from a seam still serialise as [].
	h.setOverrides(AuthOverridesView{})
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &st); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st["authOverrides"], map[string]any{"env": []any{}, "settings": []any{}, "settingsPath": ""}) {
		t.Errorf("authOverrides = %v", st["authOverrides"])
	}
}

// The served page has no warning line for a set autoswitch.model and names
// the key nowhere: the setting is gone (DESIGN A97).
func TestStaticPageHasNoModelSettingWarning(t *testing.T) {
	for _, name := range []string{"index.html", "app.js"} {
		file := staticFile(t, name)
		for _, gone := range []string{"auto-model-warning", "session following is off", "Count model limits"} {
			if strings.Contains(file, gone) {
				t.Errorf("%s still has %q", name, gone)
			}
		}
		if regexp.MustCompile(`autoswitch\.model\b`).MatchString(file) {
			t.Errorf("%s still names autoswitch.model", name)
		}
	}
}
