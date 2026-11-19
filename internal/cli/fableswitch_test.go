// Tests for `tycswap switch --store` (DESIGN A100).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
)

func fableLogin(access string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"rt-` + access + `","expiresAt":4102444800000,"subscriptionType":"max"}}`
}

// fableCLISwitcher is the fixture store with Fable logins for alice (1) and
// bob (2), both on Max as their default logins say, and alice's live in the
// Fable store.
func fableCLISwitcher(t *testing.T) *core.Switcher {
	t.Helper()
	sw := fixtureSwitcher(t)
	sw.Store.Platform = platform.Linux
	f, err := sw.Store.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	for num, email := range map[string]string{"1": "alice@example.com", "2": "bob@example.com"} {
		withDefaultPlan(t, sw, num, email, "max")
		if err := f.WriteAccountCredentials(num, email, fableLogin("f"+num)); err != nil {
			t.Fatal(err)
		}
	}
	live := filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte(fableLogin("f1")), 0o600); err != nil {
		t.Fatal(err)
	}
	useSwitcher(t, sw)
	return sw
}

// withDefaultPlan names plan in account num's default login, its tokens kept:
// the Fable store takes an account whose plan offers Fable (DESIGN A101).
func withDefaultPlan(t *testing.T, sw *core.Switcher, num, email, plan string) {
	t.Helper()
	creds, err := sw.Store.ReadAccountCredentials(num, email)
	var m map[string]map[string]any
	if err != nil || json.Unmarshal([]byte(creds), &m) != nil || m["claudeAiOauth"] == nil {
		t.Fatalf("Account-%s's default login is not an OAuth login (%v)", num, err)
	}
	m["claudeAiOauth"]["subscriptionType"] = plan
	raw, _ := json.Marshal(m)
	if err := sw.Store.WriteAccountCredentials(num, email, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchStoreFableForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"switch", []string{"switch", "--store", "fable", "2"}, "Switched the Fable store to Account-2 (bob@example.com).\n"},
		{"id first, alias", []string{"switch", "dev", "--store=fable"}, "Switched the Fable store to Account-2 (bob@example.com).\n"},
		{"already", []string{"switch", "--store", "fable", "1"}, "The Fable store is already on Account-1.\n"},
		{"empty", []string{"switch", "--store", "fable", "--empty"}, "The Fable store is off now; Fable sessions start on the default login.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw := fableCLISwitcher(t)
			before, _ := os.ReadFile(filepath.Join(sw.Home, ".claude", ".credentials.json"))
			code, out, errb := runHuman(t, tc.argv...)
			if code != 0 || errb != "" {
				t.Fatalf("exit %d, stderr %q", code, errb)
			}
			if !strings.HasPrefix(out, tc.want) {
				t.Fatalf("stdout %q, want %q", out, tc.want)
			}
			if after, _ := os.ReadFile(filepath.Join(sw.Home, ".claude", ".credentials.json")); string(after) != string(before) {
				t.Fatal("the default login changed")
			}
		})
	}
}

// --empty on a store that holds no login says so: on an emptied store and on
// a box without a Fable login, where it creates nothing (DESIGN A100).
func TestSwitchStoreFableEmptyWithoutALogin(t *testing.T) {
	const want = "The Fable store holds no login; Fable sessions start on the default login.\n"
	t.Run("emptied store", func(t *testing.T) {
		fableCLISwitcher(t)
		if code, _, errb := runHuman(t, "switch", "--store", "fable", "--empty"); code != 0 {
			t.Fatalf("first --empty: exit %d, stderr %q", code, errb)
		}
		if code, out, errb := runHuman(t, "switch", "--store", "fable", "--empty"); code != 0 || out != want {
			t.Fatalf("exit %d, stdout %q, stderr %q; want %q", code, out, errb, want)
		}
	})
	t.Run("no Fable login", func(t *testing.T) {
		sw := fixtureSwitcher(t)
		useSwitcher(t, sw)
		if code, out, errb := runHuman(t, "switch", "--store", "fable", "--empty"); code != 0 || out != want {
			t.Fatalf("exit %d, stdout %q, stderr %q; want %q", code, out, errb, want)
		}
		for _, dir := range []string{paths.FableScopeDir(sw.BackupDir()), filepath.Join(sw.BackupDir(), "credentials-fable")} {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("%s exists after --empty without a Fable login (%v)", dir, err)
			}
		}
	})
}

// A switch of the Fable store by hand from the command line logs one line
// per move with the command's actor, as the default switch does; no line
// reads by hand (DESIGN A100, reference LOG).
func TestSwitchStoreFableLogsTheCommandsActor(t *testing.T) {
	sw := fableCLISwitcher(t)
	for _, argv := range [][]string{{"switch", "--store", "fable", "2"}, {"switch", "--store", "fable", "--empty"}} {
		if code, _, errb := runHuman(t, argv...); code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", argv, code, errb)
		}
	}
	log, _ := os.ReadFile(filepath.Join(sw.BackupDir(), "tycswap.log"))
	for _, line := range []string{"Switched the Fable store to account 2 by cli(pid=", "Emptied the Fable store by cli(pid="} {
		if n := strings.Count(string(log), line); n != 1 {
			t.Errorf("%d lines %q, want one:\n%s", n, line, log)
		}
	}
	if strings.Contains(string(log), "by hand") {
		t.Errorf("a line reads by hand:\n%s", log)
	}
}

func TestSwitchStoreFableJSON(t *testing.T) {
	for _, tc := range []struct {
		argv           []string
		reason         string
		switched, toOK bool
	}{
		{[]string{"switch", "--store", "fable", "2", "--json"}, "switched", true, true},
		{[]string{"switch", "--store", "fable", "--empty", "--json"}, "emptied", true, false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			fableCLISwitcher(t)
			code, out, errb := runSub(t, tc.argv...)
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, errb)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("stdout %q: %v", out, err)
			}
			if doc["store"] != "fable" || doc["reason"] != tc.reason || doc["switched"] != tc.switched || doc["schemaVersion"] == nil {
				t.Fatalf("document %v", doc)
			}
			if to, _ := doc["to"].(map[string]any); (to != nil) != tc.toOK {
				t.Fatalf("to %v", doc["to"])
			}
		})
	}
}

func TestSwitchStoreArgumentErrors(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"switch", "--store", "fable"}, "tycswap: error: --store fable takes an account or --empty\n"},
		{[]string{"switch", "--store", "fable", "--strategy", "best"}, "tycswap: error: --store fable takes an account or --empty\n"},
		{[]string{"switch", "--store", "fable", "2", "--force"}, "tycswap: error: --store fable takes an account or --empty\n"},
		{[]string{"switch", "--store", "fable", "2", "--empty"}, "tycswap: error: --store fable takes an account or --empty\n"},
		{[]string{"switch", "--store", "codex", "2"}, "tycswap: error: unknown store: codex\n"},
		{[]string{"switch", "--store=", "2"}, "tycswap: error: unknown store: \n"},
		{[]string{"switch", "--empty"}, "tycswap: error: --empty can only be used with 'switch --store fable'\n"},
		{[]string{"list", "--store", "fable"}, "tycswap: error: --store can only be used with 'switch'\n"},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			cleanHome(t)
			code, out, errb := runSub(t, tc.argv...)
			if code != 2 || errb != tc.want || out != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 2 and %q", code, out, errb, tc.want)
			}
		})
	}
}

// permutations is every order of units, each unit's tokens kept together.
func permutations(units [][]string) [][]string {
	if len(units) == 1 {
		return [][]string{append([]string(nil), units[0]...)}
	}
	var out [][]string
	for i, head := range units {
		rest := append(append([][]string(nil), units[:i]...), units[i+1:]...)
		for _, tail := range permutations(rest) {
			out = append(out, append(append([]string(nil), head...), tail...))
		}
	}
	return out
}

// `--store fable`, the account, --json and --debug may come in any order;
// an account with --empty is the one documented line in every order
// (DESIGN A100).
func TestSwitchStoreFableFlagOrders(t *testing.T) {
	for _, store := range [][]string{{"--store", "fable"}, {"--store=fable"}} {
		for _, argv := range permutations([][]string{store, {"2"}, {"--json"}, {"--debug"}}) {
			t.Run(strings.Join(argv, " "), func(t *testing.T) {
				fableCLISwitcher(t)
				code, out, errb := runSub(t, append([]string{"switch"}, argv...)...)
				var doc struct {
					Store string         `json:"store"`
					To    map[string]any `json:"to"`
				}
				if err := json.Unmarshal([]byte(out), &doc); code != 0 || err != nil || doc.Store != "fable" || doc.To["number"] != 2.0 {
					t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errb)
				}
			})
		}
		for _, argv := range permutations([][]string{store, {"2"}, {"--empty"}}) {
			t.Run(strings.Join(argv, " "), func(t *testing.T) {
				fableCLISwitcher(t)
				code, out, errb := runSub(t, append([]string{"switch"}, argv...)...)
				if want := "tycswap: error: --store fable takes an account or --empty\n"; code != 2 || out != "" || errb != want {
					t.Fatalf("exit %d, stdout %q, stderr %q; want 2 and %q", code, out, errb, want)
				}
			})
		}
	}
	// Without --store fable the account still comes first, as in v0.15.1.
	cleanHome(t)
	for _, argv := range [][]string{{"switch", "--json", "2"}, {"switch", "--json", "--store", "default", "2"}} {
		want := "usage: tycswap <command> [args] [options]\ntycswap: error: unrecognized arguments: 2\n"
		if code, out, errb := runSub(t, argv...); code != 2 || out != "" || errb != want {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want 2 and %q", argv, code, out, errb, want)
		}
	}
}

// `--store default` is the switch as it is without --store.
func TestSwitchStoreDefaultIsTheSwitch(t *testing.T) {
	outputs := map[string]string{}
	// One subtest per form: useSwitcher replaces newSwitcher, which
	// fixtureSwitcher itself calls, until its test ends.
	for _, form := range []struct {
		name string
		argv []string
	}{
		{"plain", []string{"switch", "2"}},
		{"default", []string{"switch", "--store", "default", "2"}},
	} {
		t.Run(form.name, func(t *testing.T) {
			sw := fixtureSwitcher(t)
			useSwitcher(t, sw)
			code, out, errb := runHuman(t, form.argv...)
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, errb)
			}
			if cur := sw.CurrentAccountNumber(); cur == nil || *cur != "2" {
				t.Fatalf("live account %v", cur)
			}
			outputs[form.name] = strings.ReplaceAll(out, sw.Home, "<HOME>")
		})
	}
	if outputs["plain"] != outputs["default"] {
		t.Fatalf("switch --store default printed\n%s\nswitch printed\n%s", outputs["default"], outputs["plain"])
	}
}

func TestSwitchStoreFableRefusalExits1(t *testing.T) {
	fableCLISwitcher(t)
	code, _, errb := runSub(t, "switch", "--store", "fable", "3")
	if code != 1 || !strings.Contains(errb, "Account-3 is an API-key account") {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
}
