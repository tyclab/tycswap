package ccsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func paths(t *testing.T) (settings, sidecar string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, "store", SidecarName)
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, raw)
	}
	return m
}

func env(m map[string]any) map[string]any {
	e, _ := m["env"].(map[string]any)
	return e
}

var gw = Profile{BaseURL: "https://gw.example.com/anthropic", Token: "gw-key-0123456789"}

// TestApplyWritesProfile: the two owned keys land with the account's values;
// every other key — unrelated top-level settings, hooks, permissions, MCP
// servers and the user's own env keys — is untouched.
func TestApplyWritesProfile(t *testing.T) {
	s, sc := paths(t)
	writeJSON(t, s, map[string]any{
		"theme":        "dark",
		"permissions":  map[string]any{"allow": []any{"Bash(ls)"}},
		"hooks":        map[string]any{"SessionStart": []any{}},
		"mcpServers":   map[string]any{"x": map[string]any{"command": "x"}},
		"apiKeyHelper": "/opt/me/helper",
		"env":          map[string]any{"FOO": "bar", "ANTHROPIC_AUTH_TOKEN": "mine", "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1"},
	})
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, s)
	want := map[string]any{
		"theme":        "dark",
		"permissions":  map[string]any{"allow": []any{"Bash(ls)"}},
		"hooks":        map[string]any{"SessionStart": []any{}},
		"mcpServers":   map[string]any{"x": map[string]any{"command": "x"}},
		"apiKeyHelper": "/opt/me/helper",
		"env": map[string]any{
			"FOO":                                    "bar",
			"ANTHROPIC_BASE_URL":                     gw.BaseURL,
			"ANTHROPIC_AUTH_TOKEN":                   gw.Token,
			"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings after Apply:\n got %v\nwant %v", got, want)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{s, sc} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %o, want 600", p, info.Mode().Perm())
			}
		}
	}
	raw, _ := os.ReadFile(s)
	if !strings.HasPrefix(string(raw), "{\n  \"") {
		t.Errorf("settings not 2-space indented:\n%s", raw)
	}
}

// TestApplyOnlyTouchesTheAllowlist: the allowlist is exactly the two env keys,
// and a settings file with every other kind of key comes out of Apply with
// all of them byte-identical once the two are set aside.
func TestApplyOnlyTouchesTheAllowlist(t *testing.T) {
	if got := OwnedKeys(); !reflect.DeepEqual(got, []string{"env.ANTHROPIC_BASE_URL", "env.ANTHROPIC_AUTH_TOKEN"}) {
		t.Fatalf("OwnedKeys = %v", got)
	}
	s, sc := paths(t)
	before := map[string]any{
		"model":               "opus",
		"autoUpdatesChannel":  "stable",
		"apiKeyHelper":        "/x",
		"statusLine":          map[string]any{"type": "command", "command": "x"},
		"enabledPlugins":      map[string]any{"a@b": true},
		"env":                 map[string]any{"ANTHROPIC_API_KEY": "sk-ant-api03-mine", "HTTPS_PROXY": "http://proxy:3128"},
		"cleanupPeriodDays":   30,
		"includeCoAuthoredBy": false,
	}
	writeJSON(t, s, before)
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	after := readJSON(t, s)
	e := env(after)
	delete(e, "ANTHROPIC_BASE_URL")
	delete(e, "ANTHROPIC_AUTH_TOKEN")
	var wantRT map[string]any
	b, _ := json.Marshal(before)
	_ = json.Unmarshal(b, &wantRT)
	if !reflect.DeepEqual(after, wantRT) {
		t.Errorf("keys outside the allowlist changed:\n got %v\nwant %v", after, wantRT)
	}
}

// TestApplyMissingRefusesCorrupt: a missing file is {}; a corrupt one is
// refused and left byte-for-byte alone (rewriting it would destroy every
// setting the profile does not own), and Revert refuses the same way.
func TestApplyMissingRefusesCorrupt(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		s, sc := paths(t)
		if err := Apply(s, sc, gw); err != nil {
			t.Fatal(err)
		}
		if got := env(readJSON(t, s))["ANTHROPIC_BASE_URL"]; got != gw.BaseURL {
			t.Errorf("ANTHROPIC_BASE_URL = %v", got)
		}
	})
	for _, tc := range []struct{ name, content string }{
		{"corrupt", "{not json"},
		{"array", "[1,2]"},
		{"null", "null"},
		{"two values", "{} {}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sc := paths(t)
			if err := os.MkdirAll(filepath.Dir(s), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			err := Apply(s, sc, gw)
			if err == nil || !strings.Contains(err.Error(), "refusing to rewrite") || !strings.Contains(err.Error(), s) {
				t.Fatalf("Apply err = %v, want a refusal naming the file", err)
			}
			if b, _ := os.ReadFile(s); string(b) != tc.content {
				t.Errorf("settings rewritten to %q", b)
			}
			if SidecarExists(sc) {
				t.Error("sidecar written although Apply refused")
			}
			if err := Check(s, sc); err == nil {
				t.Error("Check passed a settings file Apply refuses")
			}
			// Revert refuses the same way rather than writing {} over it.
			if err := os.MkdirAll(filepath.Dir(sc), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sc, []byte(`{"version":1,"settingsPath":`+jsonString(s)+`,"keys":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Revert(s, sc, nil); err == nil {
				t.Error("Revert rewrote a corrupt settings file")
			}
			if b, _ := os.ReadFile(s); string(b) != tc.content {
				t.Errorf("Revert changed the file to %q", b)
			}
		})
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestRevertRestoresExactly: an absent key is deleted, a present one restored
// to its prior value (a non-string included), unrelated keys survive with the
// user's later edits, an owned key the user edited after Apply is still
// reverted (the record wins), and the sidecar is gone.
func TestRevertRestoresExactly(t *testing.T) {
	s, sc := paths(t)
	writeJSON(t, s, map[string]any{
		"theme": "dark",
		"env": map[string]any{
			"ANTHROPIC_AUTH_TOKEN": float64(5),
			"OTHER":                "x",
		},
	})
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, s)
	m["theme"] = "light"
	m["model"] = "sonnet"
	env(m)["ANTHROPIC_BASE_URL"] = "https://edited.example"
	env(m)["MINE"] = "1"
	writeJSON(t, s, m)

	out, err := Revert(s, sc, nil)
	if err != nil || out != RevertedFromRecord {
		t.Fatalf("Revert = %v, %v", out, err)
	}
	want := map[string]any{
		"theme": "light",
		"model": "sonnet",
		"env": map[string]any{
			"ANTHROPIC_AUTH_TOKEN": float64(5),
			"OTHER":                "x",
			"MINE":                 "1",
		},
	}
	if got := readJSON(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("after revert:\n got %v\nwant %v", got, want)
	}
	if _, err := os.Stat(sc); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sidecar should be removed, stat err = %v", err)
	}
	if IsApplied(sc) {
		t.Error("IsApplied should be false after revert")
	}
}

// TestRevertRemovesCreatedEnvContainer: with no env before and nothing else
// added, revert deletes the container; a key the user added meanwhile keeps
// it, with only that key.
func TestRevertRemovesCreatedEnvContainer(t *testing.T) {
	t.Run("empty after revert", func(t *testing.T) {
		s, sc := paths(t)
		writeJSON(t, s, map[string]any{"theme": "dark"})
		if err := Apply(s, sc, gw); err != nil {
			t.Fatal(err)
		}
		if _, err := Revert(s, sc, nil); err != nil {
			t.Fatal(err)
		}
		if got := readJSON(t, s); !reflect.DeepEqual(got, map[string]any{"theme": "dark"}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("user key keeps container", func(t *testing.T) {
		s, sc := paths(t)
		writeJSON(t, s, map[string]any{})
		if err := Apply(s, sc, gw); err != nil {
			t.Fatal(err)
		}
		m := readJSON(t, s)
		env(m)["MINE"] = "1"
		writeJSON(t, s, m)
		if _, err := Revert(s, sc, nil); err != nil {
			t.Fatal(err)
		}
		if got := readJSON(t, s); !reflect.DeepEqual(got, map[string]any{"env": map[string]any{"MINE": "1"}}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("non-object env is put back", func(t *testing.T) {
		s, sc := paths(t)
		writeJSON(t, s, map[string]any{"env": "weird"})
		if err := Apply(s, sc, gw); err != nil {
			t.Fatal(err)
		}
		if _, err := Revert(s, sc, nil); err != nil {
			t.Fatal(err)
		}
		if got := readJSON(t, s); !reflect.DeepEqual(got, map[string]any{"env": "weird"}) {
			t.Errorf("got %v", got)
		}
	})
}

// TestDoubleApplyKeepsOriginalPrior: one endpoint, then another, then revert
// lands on the settings from before the first, not on the first profile.
func TestDoubleApplyKeepsOriginalPrior(t *testing.T) {
	s, sc := paths(t)
	writeJSON(t, s, map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://orig.example"}})
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	second := Profile{BaseURL: "https://two.example", Token: "two-key"}
	if err := Apply(s, sc, second); err != nil {
		t.Fatal(err)
	}
	if got := env(readJSON(t, s)); got["ANTHROPIC_BASE_URL"] != second.BaseURL || got["ANTHROPIC_AUTH_TOKEN"] != second.Token {
		t.Errorf("second apply not written: %v", got)
	}
	if _, err := Revert(s, sc, nil); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, s); !reflect.DeepEqual(got, map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://orig.example"}}) {
		t.Errorf("revert after double apply = %v, want the ORIGINAL priors", got)
	}
}

// TestApplyIsIdempotent: applying the same profile again changes neither the
// settings file nor the record.
func TestApplyIsIdempotent(t *testing.T) {
	s, sc := paths(t)
	writeJSON(t, s, map[string]any{"theme": "dark", "env": map[string]any{"A": "b"}})
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	s1, _ := os.ReadFile(s)
	c1, _ := os.ReadFile(sc)
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	s2, _ := os.ReadFile(s)
	c2, _ := os.ReadFile(sc)
	if !bytes.Equal(s1, s2) || !bytes.Equal(c1, c2) {
		t.Errorf("a second Apply changed the files:\n%s\n%s\n--\n%s\n%s", s1, c1, s2, c2)
	}
}

// TestRevertWithoutSidecar: with no sidecar and nothing recognisably ours in
// the file, Revert reports nothing and leaves the file alone.
func TestRevertWithoutSidecar(t *testing.T) {
	s, sc := paths(t)
	writeJSON(t, s, map[string]any{"a": "b", "env": map[string]any{"ANTHROPIC_BASE_URL": gw.BaseURL, "ANTHROPIC_AUTH_TOKEN": "someone-else"}})
	before, _ := os.ReadFile(s)
	for _, known := range [][]Profile{nil, {gw}} {
		out, err := Revert(s, sc, known)
		if err != nil || out != RevertedNothing {
			t.Fatalf("Revert(known=%v) = %v, %v; want RevertedNothing, nil", known, out, err)
		}
	}
	if after, _ := os.ReadFile(s); !bytes.Equal(before, after) {
		t.Error("settings file was rewritten by a no-op revert")
	}
	if IsApplied(sc) || SidecarExists(sc) {
		t.Error("no sidecar must read as not applied")
	}
}

// TestRevertByValue: a profile whose record was lost is still taken out when
// settings.json holds exactly a known endpoint and its key; a pair that is
// not tycswap's, or only half of one, stays.
func TestRevertByValue(t *testing.T) {
	t.Run("known pair", func(t *testing.T) {
		s, sc := paths(t)
		writeJSON(t, s, map[string]any{"theme": "dark"})
		if err := Apply(s, sc, gw); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(sc); err != nil {
			t.Fatal(err)
		}
		other := Profile{BaseURL: "https://other.example", Token: "other"}
		out, err := Revert(s, sc, []Profile{other, gw})
		if err != nil || out != RevertedByValue {
			t.Fatalf("Revert = %v, %v; want RevertedByValue", out, err)
		}
		if got := readJSON(t, s); !reflect.DeepEqual(got, map[string]any{"theme": "dark"}) {
			t.Errorf("settings = %v, want just the user's own key back", got)
		}
	})
	for _, tc := range []struct {
		name string
		env  map[string]any
	}{
		{"foreign endpoint", map[string]any{"ANTHROPIC_BASE_URL": "https://acme.example", "ANTHROPIC_AUTH_TOKEN": gw.Token}},
		{"token changed", map[string]any{"ANTHROPIC_BASE_URL": gw.BaseURL, "ANTHROPIC_AUTH_TOKEN": "changed"}},
		{"url only", map[string]any{"ANTHROPIC_BASE_URL": gw.BaseURL}},
		{"token only", map[string]any{"ANTHROPIC_AUTH_TOKEN": gw.Token}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sc := paths(t)
			writeJSON(t, s, map[string]any{"env": tc.env})
			before, _ := os.ReadFile(s)
			if out, err := Revert(s, sc, []Profile{gw}); err != nil || out != RevertedNothing {
				t.Fatalf("Revert = %v, %v; want RevertedNothing", out, err)
			}
			if after, _ := os.ReadFile(s); !bytes.Equal(before, after) {
				t.Errorf("a pair that is not ours was touched: %s", after)
			}
		})
	}
}

// TestCorruptSidecarIsAnError: Apply, Revert and Check refuse rather than
// guess, and say which file it is.
func TestCorruptSidecarIsAnError(t *testing.T) {
	s, sc := paths(t)
	writeJSON(t, s, map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": gw.BaseURL, "ANTHROPIC_AUTH_TOKEN": gw.Token}})
	if err := os.MkdirAll(filepath.Dir(sc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sc, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s)
	if err := Apply(s, sc, gw); err == nil || !strings.Contains(err.Error(), "corrupt") || !strings.Contains(err.Error(), sc) {
		t.Errorf("Apply err = %v, want a corrupt-sidecar error naming it", err)
	}
	if _, err := Revert(s, sc, []Profile{gw}); err == nil {
		t.Error("Revert with a corrupt sidecar should error, not fall back to by-value")
	}
	if err := Check(s, sc); err == nil {
		t.Error("Check passed a corrupt sidecar")
	}
	if IsApplied(sc) {
		t.Error("IsApplied must be false for a corrupt sidecar")
	}
	if !SidecarExists(sc) {
		t.Error("SidecarExists must see a corrupt sidecar")
	}
	if after, _ := os.ReadFile(s); !bytes.Equal(before, after) {
		t.Errorf("settings changed: %s", after)
	}
}

// TestRevertUsesTheRecordedFile: the record names the settings file it was
// taken from, and Revert restores that one.
func TestRevertUsesTheRecordedFile(t *testing.T) {
	s, sc := paths(t)
	writeJSON(t, s, map[string]any{"k": "v"})
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	if got := RecordedSettingsPath(sc); got != s {
		t.Errorf("RecordedSettingsPath = %q, want %q", got, s)
	}
	elsewhere := filepath.Join(t.TempDir(), "settings.json")
	if _, err := Revert(elsewhere, sc, nil); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, s); !reflect.DeepEqual(got, map[string]any{"k": "v"}) {
		t.Errorf("got %v", got)
	}
	if _, err := os.Stat(elsewhere); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Revert wrote a file it had no record for: %v", err)
	}
}

// TestApplyToAnotherFileRevertsTheFirst: a record for another settings file
// (CLAUDE_CONFIG_DIR changed between two switches) is reverted there before
// the new file is written, so no profile is left behind without a record.
func TestApplyToAnotherFileRevertsTheFirst(t *testing.T) {
	s1, sc := paths(t)
	s2 := filepath.Join(t.TempDir(), "other", "settings.json")
	writeJSON(t, s1, map[string]any{"one": true})
	writeJSON(t, s2, map[string]any{"two": true})
	if err := Apply(s1, sc, gw); err != nil {
		t.Fatal(err)
	}
	if err := Check(s2, sc); err != nil {
		t.Fatal(err)
	}
	if err := Apply(s2, sc, gw); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, s1); !reflect.DeepEqual(got, map[string]any{"one": true}) {
		t.Errorf("first file = %v, want it reverted", got)
	}
	if got := RecordedSettingsPath(sc); got != s2 {
		t.Errorf("record names %q, want %q", got, s2)
	}
	if _, err := Revert("", sc, nil); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, s2); !reflect.DeepEqual(got, map[string]any{"two": true}) {
		t.Errorf("second file = %v, want it reverted", got)
	}
}

// TestSidecarShape pins the recorded schema: version, settings path, and
// present/absent with the value for each owned key and the env container.
func TestSidecarShape(t *testing.T) {
	s, sc := paths(t)
	writeJSON(t, s, map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://before.example"}})
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	raw := readJSON(t, sc)
	if raw["version"] != float64(1) || raw["settingsPath"] != s {
		t.Errorf("sidecar header = %v", raw)
	}
	keys := raw["keys"].(map[string]any)
	if len(keys) != len(ownedKeys)+1 {
		t.Errorf("sidecar records %d keys, want the %d owned keys and the env container: %v", len(keys), len(ownedKeys), keys)
	}
	base := keys["env.ANTHROPIC_BASE_URL"].(map[string]any)
	if base["present"] != true || base["value"] != "https://before.example" {
		t.Errorf("base URL prior = %v", base)
	}
	if tok := keys["env.ANTHROPIC_AUTH_TOKEN"].(map[string]any); tok["present"] != false {
		t.Errorf("token prior should be absent: %v", tok)
	}
	if e := keys["env"].(map[string]any); e["present"] != true {
		t.Errorf("env container prior should be present: %v", e)
	}
}

// TestNumbersSurviveARewrite: a number in a key the profile does not own is
// written back as it was, however large.
func TestNumbersSurviveARewrite(t *testing.T) {
	s, sc := paths(t)
	if err := os.MkdirAll(filepath.Dir(s), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s, []byte(`{"big": 12345678901234567890, "f": 1.50}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s)
	if !strings.Contains(string(raw), "12345678901234567890") || !strings.Contains(string(raw), "1.50") {
		t.Errorf("numbers changed:\n%s", raw)
	}
	if _, err := Revert("", sc, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(s)
	if !strings.Contains(string(raw), "12345678901234567890") {
		t.Errorf("numbers changed by the revert:\n%s", raw)
	}
}

// TestLive reads the two keys as they are now.
func TestLive(t *testing.T) {
	s, sc := paths(t)
	if got := Live(s); got != (Profile{}) {
		t.Errorf("Live(missing) = %v", got)
	}
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	if got := Live(s); got != gw {
		t.Errorf("Live = %v, want %v", got, gw)
	}
}

// TestSnapshotRestore: a snapshot puts back a present file's bytes and mode
// and removes a file that was absent, so a failed switch leaves both
// settings.json and the record as they were.
func TestSnapshotRestore(t *testing.T) {
	s, sc := paths(t)
	if err := os.MkdirAll(filepath.Dir(s), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\"theme\": \"dark\"}\n")
	if err := os.WriteFile(s, original, 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := Take(s, sc, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(s, sc, gw); err != nil {
		t.Fatal(err)
	}
	if err := snap.Restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(s); !bytes.Equal(got, original) {
		t.Errorf("settings = %q, want the original bytes", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(s); info.Mode().Perm() != 0o644 {
			t.Errorf("settings mode = %o, want the original 644", info.Mode().Perm())
		}
	}
	if SidecarExists(sc) {
		t.Error("the record Apply wrote survived the restore")
	}
	var nilSnap *Snapshot
	if err := nilSnap.Restore(); err != nil {
		t.Errorf("nil snapshot Restore = %v", err)
	}
}

// TestApplyRefusesAnInvalidProfile: the values are checked before anything
// is written.
func TestApplyRefusesAnInvalidProfile(t *testing.T) {
	for _, p := range []Profile{
		{BaseURL: "", Token: "k"},
		{BaseURL: "ftp://x.example", Token: "k"},
		{BaseURL: gw.BaseURL, Token: ""},
		{BaseURL: gw.BaseURL, Token: "two words"},
	} {
		s, sc := paths(t)
		if err := Apply(s, sc, p); err == nil {
			t.Errorf("Apply(%+v) succeeded", p)
		}
		if _, err := os.Stat(s); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Apply(%+v) wrote settings", p)
		}
		if SidecarExists(sc) {
			t.Errorf("Apply(%+v) wrote a record", p)
		}
	}
}
