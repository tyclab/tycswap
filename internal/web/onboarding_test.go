// Tests for the first page's onboarding (DESIGN A27): the live login that is
// not stored yet, the auth-overrides notice, the Add current login button as
// the Accounts card's main action, and the Guide and Settings tabs' markup.
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
	// The three variables, in their order; empty ones select nothing.
	v = DetectAuthOverrides(getenv(map[string]string{"ANTHROPIC_BASE_URL": "https://proxy.example", "ANTHROPIC_API_KEY": "  ", "ANTHROPIC_AUTH_TOKEN": "t"}), path)
	if !reflect.DeepEqual(v.Env, []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"}) || len(v.Settings) != 0 {
		t.Errorf("env = %+v", v)
	}
	// The settings file's env block and apiKeyHelper; blanks and nulls skipped.
	if err := os.WriteFile(path, []byte(`{"apiKeyHelper": "/usr/local/bin/key.sh", "env": {"ANTHROPIC_API_KEY": "sk-x", "ANTHROPIC_AUTH_TOKEN": "", "ANTHROPIC_BASE_URL": null, "OTHER": "1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	v = DetectAuthOverrides(getenv(nil), path)
	if !reflect.DeepEqual(v.Settings, []string{"apiKeyHelper", "env.ANTHROPIC_API_KEY"}) || len(v.Env) != 0 {
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

func TestIndexHTML_OnboardingMarkup(t *testing.T) {
	index := staticFile(t, "index.html")
	for _, id := range []string{"onboard", "auth-overrides", "auth-overrides-env", "auth-overrides-env-keys", "auth-overrides-settings", "auth-overrides-settings-keys", "auth-overrides-path", "add-callout", "add-callout-email", "add-hint", "accounts-card"} {
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Errorf("element #%s missing", id)
		}
	}
	// Add current login is the Accounts card's main button: a primary button
	// in the card head, and the callout's big one; no longer in the toolbar.
	head := regexp.MustCompile(`(?s)<div class="card" id="accounts-card">.*?<div class="toolbar"`).FindString(index)
	if head == "" || !strings.Contains(head, `class="btn btn-primary" data-action="add-current"`) {
		t.Error("the Accounts card head lacks the primary Add current login button")
	}
	toolbar := regexp.MustCompile(`(?s)<div class="toolbar".*?</div>`).FindString(index)
	if strings.Contains(toolbar, `data-action="add-current"`) {
		t.Error("Add current login is still a toolbar button")
	}
	if !strings.Contains(index, "Do not type <code>/logout</code> first") {
		t.Error("the add hint does not warn against /logout")
	}
	js := staticFile(t, "app.js")
	for _, needle := range []string{"function renderOnboarding(", "st.currentLogin", "add-callout-email", "auth-overrides-env-keys", "auth-overrides-settings-keys"} {
		if !strings.Contains(js, needle) {
			t.Errorf("app.js lacks %q", needle)
		}
	}
	if strings.Contains(js, "confirmModal('Add current login'") {
		t.Error("Add current login still asks for a confirmation")
	}
}

// The Guide tab: every section the design names, generic text, no URL (the
// page works offline), and Codex where the tool supports it.
func TestIndexHTML_GuideTab(t *testing.T) {
	index := staticFile(t, "index.html")
	guide := regexp.MustCompile(`(?s)<section class="panel" id="panel-guide".*?</section>`).FindString(index)
	if guide == "" {
		t.Fatal("no guide panel")
	}
	for _, id := range []string{"g-what", "g-concepts", "g-start", "g-switch", "g-auto", "g-settings", "g-sessions", "g-updates", "g-codex", "g-cli", "g-trouble", "g-privacy"} {
		if !strings.Contains(guide, `id="`+id+`"`) {
			t.Errorf("guide section #%s missing", id)
		}
		if !strings.Contains(guide, `href="#`+id+`"`) {
			t.Errorf("guide contents do not link #%s", id)
		}
	}
	for _, needle := range []string{"{{.Name}} config", "{{.Name}} upgrade", "{{.Name}} codex", "add --login", "/logout", "autoswitch.threshold", "autoswitch.model", "5-hour", "7-day", "sessions", "127.0.0.1"} {
		if !strings.Contains(guide, needle) {
			t.Errorf("guide lacks %q", needle)
		}
	}
	if strings.Contains(guide, "telemetry") && !strings.Contains(guide, "There is no telemetry") {
		t.Error("the guide mentions telemetry other than to say there is none")
	}
	if !strings.Contains(index, `data-tab="guide"`) || !strings.Contains(index, `id="tab-guide"`) {
		t.Error("no guide tab")
	}
}

// The Settings tab: its own tab, every key from the state, through the
// existing settings routes; the Auto tab keeps a short way there and no
// duplicate grid.
func TestIndexHTML_SettingsTab(t *testing.T) {
	index := staticFile(t, "index.html")
	panel := regexp.MustCompile(`(?s)<section class="panel" id="panel-settings".*?</section>`).FindString(index)
	if panel == "" {
		t.Fatal("no settings panel")
	}
	for _, id := range []string{"settings-card", "settings-sub", "settings-grid", "settings-empty"} {
		if !strings.Contains(panel, `id="`+id+`"`) {
			t.Errorf("settings panel lacks #%s", id)
		}
	}
	auto := regexp.MustCompile(`(?s)<section class="panel" id="panel-auto".*?</section>\s*<!-- =+ Sessions`).FindString(index)
	if auto == "" {
		t.Fatal("no auto panel")
	}
	if strings.Contains(auto, `class="settings-grid"`) {
		t.Error("the Auto tab still carries a settings grid")
	}
	if !strings.Contains(auto, `href="#settings"`) {
		t.Error("the Auto tab has no link to the Settings tab")
	}
	if !strings.Contains(index, `id="badge-settings"`) {
		t.Error("no settings tab badge")
	}
	js := staticFile(t, "app.js")
	for _, needle := range []string{"$('settings-grid')", "$('settings-empty')", "renderGuarded('settings', 'panel-settings'", "function settingRow(", "function settingControl(", "'badge-settings'", "sv.applies", "'in effect '"} {
		if !strings.Contains(js, needle) {
			t.Errorf("app.js lacks %q", needle)
		}
	}
	// The grid depends on the settings facade alone, never on the engine.
	if m := regexp.MustCompile(`function renderSettings\(st\) \{[\s\S]*?\n  \}`).FindString(js); strings.Contains(m, "st.auto") {
		t.Error("renderSettings reads the auto engine's state")
	}
}

// The "limits ignored" warning says where model windows are counted: the
// Count model limits switch on the Auto tab, or autoswitch.model on the
// Settings tab. The key left the Auto tab with the settings editor, so the
// warning must not send the user there to set it.
func TestStaticIgnoredModelsNoteNamesBothPlaces(t *testing.T) {
	note := regexp.MustCompile(`function ignoredModelsNote\(st, models\) \{[\s\S]*?\n  \}`).FindString(staticFile(t, "app.js"))
	if note == "" {
		t.Fatal("no ignoredModelsNote in app.js")
	}
	for _, want := range []string{`"Count model limits" on the Auto tab`, "autoswitch.model on the Settings tab"} {
		if !strings.Contains(note, want) {
			t.Errorf("the warning lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "autoswitch.model on the Auto tab") {
		t.Error("the warning sends the user to the Auto tab to set autoswitch.model")
	}
}
