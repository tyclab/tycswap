// The no-change rule (DESIGN A100): with no Fable login, no command creates
// the Fable scope and no document gains a field.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
)

// noFableKeyPaths are the JSON key paths v0.15.1 printed for these commands
// on the fixture store, captured from origin/main.
var noFableKeyPaths = map[string]string{
	"list --json":                  "accounts accounts[].active accounts[].alias accounts[].disabled accounts[].email accounts[].isOrganization accounts[].number accounts[].organizationName accounts[].organizationUuid accounts[].usage accounts[].usageStatus activeAccountNumber schemaVersion",
	"status --json":                "active active.email active.isOrganization active.managed active.number active.organizationName active.organizationUuid active.usage active.usageStatus schemaVersion totalManagedAccounts",
	"auto --once --json --dry-run": "active active.email active.number detail event headroomPct headroomPct.1 headroomPct.2 headroomPct.3 headroomPct.5 reason schemaVersion threshold ts",
	"switch 2 --json":              "from from.email from.number message reason schemaVersion strategy switched to to.email to.number warnings",
	"switch 2":                     "",
}

// keyPaths lists every key path of a JSON document (array elements as []).
func keyPaths(v any, prefix string, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			keyPaths(child, p, out)
		}
	case []any:
		for _, child := range x {
			keyPaths(child, prefix+"[]", out)
		}
	}
}

// documentKeyPaths is the sorted key paths of every JSON document in out.
func documentKeyPaths(out string) string {
	paths := map[string]bool{}
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var doc any
		if dec.Decode(&doc) != nil {
			break
		}
		keyPaths(doc, "", paths)
	}
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, " ")
}

var volatile = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z?|\d{10}(\.\d+)?`)

func TestNoFableLoginNoChange(t *testing.T) {
	record := os.Getenv("TYCSWAP_NOFABLE_RECORD")
	got := map[string]string{}
	for _, argv := range [][]string{
		{"list", "--json"},
		{"status", "--json"},
		{"auto", "--once", "--json", "--dry-run"},
		{"switch", "2", "--json"},
		{"switch", "2"},
	} {
		name := strings.Join(argv, " ")
		sw := fixtureSwitcher(t)
		if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.enabled", "true"); err != nil {
			t.Fatal(err)
		}
		useSwitcher(t, sw)
		code, out, errb := runHuman(t, argv...)
		for _, dir := range []string{paths.FableScopeDir(sw.BackupDir()), paths.FableCredentialsDir(sw.BackupDir())} {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("%s: %s exists (%v)", name, dir, err)
			}
		}
		if want := map[string]int{"auto --once --json --dry-run": 2}[name]; code != want {
			t.Errorf("%s: exit %d, want %d (stderr %q)", name, code, want, errb)
		}
		keys := documentKeyPaths(out)
		if record == "" && keys != noFableKeyPaths[name] {
			t.Errorf("%s key paths\n got %s\nwant %s", name, keys, noFableKeyPaths[name])
		}
		out = volatile.ReplaceAllString(strings.ReplaceAll(out, sw.Home, "<HOME>"), "<T>")
		errb = volatile.ReplaceAllString(strings.ReplaceAll(errb, sw.Home, "<HOME>"), "<T>")
		got[name] = "=== " + name + " | exit " + strconv.Itoa(code) + " | keys: " + keys + "\n--- stdout\n" + out + "--- stderr\n" + errb
		for _, key := range []string{`"fable"`, `"fableAccountNumber"`, `"fableLogin"`, `"store"`} {
			if strings.Contains(out, key) {
				t.Errorf("%s printed %s:\n%s", name, key, out)
			}
		}
	}
	if record != "" {
		var b strings.Builder
		for _, name := range []string{"list --json", "status --json", "auto --once --json --dry-run", "switch 2 --json", "switch 2"} {
			b.WriteString(got[name])
		}
		if err := os.WriteFile(filepath.Clean(record), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
