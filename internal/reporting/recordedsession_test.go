// Tests for the session record reader lanes C and D use (DESIGN A100).
package reporting

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordedSession(t *testing.T) {
	root := t.TempDir()
	doc := `{"schemaVersion":1,"sessions":{` +
		`"s-default":{"family":"opus","seenAt":1},` +
		`"s-fable":{"family":"fable","seenAt":2,"store":"fable"}}}`
	if err := os.WriteFile(filepath.Join(root, "default_models.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, family, store string
		ok                bool
	}{
		{"s-default", "opus", "", true},
		{"s-fable", "fable", "fable", true},
		{"s-missing", "", "", false},
	} {
		family, store, ok := RecordedSession(root, tc.id)
		if family != tc.family || store != tc.store || ok != tc.ok {
			t.Errorf("RecordedSession(%s) = %q, %q, %v; want %q, %q, %v", tc.id, family, store, ok, tc.family, tc.store, tc.ok)
		}
	}
	if _, _, ok := RecordedSession(t.TempDir(), "s-default"); ok {
		t.Error("a store without default_models.json has a record")
	}
}

func TestModelFamilyExported(t *testing.T) {
	for model, want := range map[string]string{"fable": "fable", "claude-fable-5-1[1m]": "fable", "Opus": "opus", "gpt-5": ""} {
		if got := ModelFamily(model); got != want {
			t.Errorf("ModelFamily(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestDefaultRecordHasNoStoreField(t *testing.T) {
	root := t.TempDir()
	if _, err := RecordDefaultSessionModel(root, "s1", "opus", "", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "default_models.json"))
	if want := `{"schemaVersion":1,"sessions":{"s1":{"family":"opus","seenAt":100}}}`; string(raw) != want {
		t.Fatalf("default_models.json = %s, want %s", raw, want)
	}
}
