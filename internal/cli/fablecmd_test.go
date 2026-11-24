// Tests for `tycswap fable` (DESIGN A107): the mark's forms, exit codes and
// JSON, and the Fable sign-in of an account whose plan field is empty.
package cli

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/testutil"
)

// newFableMarkFixture is newFableLoginFixture plus Account-3
// (c@example.com), added by `add --login` with an empty plan field.
func newFableMarkFixture(t *testing.T) *loginFixture {
	t.Helper()
	f := newFableLoginFixture(t)
	testutil.Setenv(t, "FAKE_CLAUDE_SUBSCRIPTION", "")
	signInAs(t, "c@example.com", "default-3")
	if code, _, errb := f.run(t, "add", "--login"); code != 0 {
		t.Fatalf("seeding Account-3: exit %d, stderr %q", code, errb)
	}
	signInAs(t, "c@example.com", "fable-3")
	return f
}

func rosterBytes(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(paths.GetBackupRoot() + "/sequence.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func fableMarkOf(t *testing.T, num string) (any, bool) {
	t.Helper()
	var roster struct {
		Accounts map[string]map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(rosterBytes(t)), &roster); err != nil {
		t.Fatal(err)
	}
	v, ok := roster.Accounts[num]["fableContinue"]
	return v, ok
}

func TestFableCommandArguments(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"fable"}, "expected --allow ID, --disallow ID, --list or --login"},
		{[]string{"fable", "--allow"}, "--allow requires an account identifier"},
		{[]string{"fable", "--disallow", "--list"}, "--disallow requires an account identifier"},
		{[]string{"fable", "--allow", "1", "--list"}, "--allow, --disallow, --list and --login are exclusive"},
		{[]string{"fable", "--list", "--disallow", "1"}, "--allow, --disallow, --list and --login are exclusive"},
		{[]string{"fable", "--allow", "1", "--json"}, "--json goes with --list"},
		{[]string{"fable", "--list", "--force"}, "unrecognized option: --force"},
		{[]string{"fable", "--list", "3"}, "unrecognized arguments: 3"},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			f := newFableMarkFixture(t)
			before := rosterBytes(t)
			code, out, errb := f.run(t, tc.argv...)
			if code != 2 || out != "" || !strings.HasSuffix(strings.TrimSpace(errb), "tycswap fable: error: "+tc.want) {
				t.Fatalf("exit %d out %q stderr %q", code, out, errb)
			}
			if rosterBytes(t) != before {
				t.Error("a refused command wrote the roster")
			}
		})
	}
}

func TestFableCommandAllowDisallowList(t *testing.T) {
	f := newFableMarkFixture(t)
	code, out, errb := f.run(t, "fable", "--list")
	if code != 0 || errb != "" || out != "1  a@example.com  Fable: yes, plan max     Fable login: no\n"+
		"2  b@example.com  Fable: yes, plan max     Fable login: no\n"+
		"3  c@example.com  Fable: no, unknown plan  Fable login: no\n" {
		t.Fatalf("--list: exit %d stderr %q out\n%s", code, errb, out)
	}

	if code, out, errb := f.run(t, "fable", "--allow", "c@example.com"); code != 0 || out != "" || errb != "" {
		t.Fatalf("--allow: exit %d out %q stderr %q", code, out, errb)
	}
	if v, ok := fableMarkOf(t, "3"); !ok || v != true {
		t.Fatalf("fableContinue = %v (present %v)", v, ok)
	}
	code, out, errb = f.run(t, "fable", "--list", "--json")
	var doc map[string]any
	if code != 0 || errb != "" || json.Unmarshal([]byte(out), &doc) != nil {
		t.Fatalf("--list --json: exit %d stderr %q out %q", code, errb, out)
	}
	want := map[string]any{"schemaVersion": 1.0, "accounts": []any{
		map[string]any{"number": 1.0, "email": "a@example.com", "fable": true, "by": "plan", "plan": "max", "fableLogin": false},
		map[string]any{"number": 2.0, "email": "b@example.com", "fable": true, "by": "plan", "plan": "max", "fableLogin": false},
		map[string]any{"number": 3.0, "email": "c@example.com", "fable": true, "by": "mark", "fableLogin": false},
	}}
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("--list --json = %v", doc)
	}

	if code, out, errb := f.run(t, "fable", "--disallow", "3"); code != 0 || out != "" || errb != "" {
		t.Fatalf("--disallow: exit %d out %q stderr %q", code, out, errb)
	}
	if v, ok := fableMarkOf(t, "3"); ok {
		t.Fatalf("fableContinue left as %v", v)
	}
	if _, err := os.Stat(paths.FableCredentialsDir(paths.GetBackupRoot())); !os.IsNotExist(err) {
		t.Errorf("the mark created credentials-fable/ (%v)", err)
	}
}

func TestFableCommandUnknownAccount(t *testing.T) {
	f := newFableMarkFixture(t)
	before := rosterBytes(t)
	for _, argv := range [][]string{{"fable", "--allow", "9"}, {"fable", "--disallow", "nobody@example.com"}} {
		code, out, errb := f.run(t, argv...)
		if code != 1 || out != "" || !strings.HasPrefix(errb, "Error: ") {
			t.Errorf("%v: exit %d out %q stderr %q", argv, code, out, errb)
		}
	}
	if rosterBytes(t) != before {
		t.Error("an unknown account wrote the roster")
	}
}

// The sign-in of an account whose plan field is empty is refused before
// the browser opens, naming the mark; after `fable --allow` it goes through.
func TestFableSignInOfAnUnknownPlanAccount(t *testing.T) {
	f := newFableMarkFixture(t)
	started := countLogins(t, f)
	code, out, errb := f.run(t, "add", "--login", "--lineage", "fable", "--slot", "3")
	want := "Error: Account-3's plan does not offer Fable. If it runs Fable, mark it first: tycswap fable --allow 3"
	if code != 1 || out != "" || strings.TrimSpace(errb) != want {
		t.Fatalf("exit %d out %q stderr %q", code, out, errb)
	}
	if len(*started) != 0 {
		t.Fatalf("claude's login started %d times before the refusal", len(*started))
	}
	if code, _, errb := f.run(t, "fable", "--allow", "3"); code != 0 {
		t.Fatalf("--allow: exit %d stderr %q", code, errb)
	}
	code, out, errb = f.run(t, "add", "--login", "--lineage", "fable", "--slot", "3")
	if code != 0 || sortedLines(out) != "Sign in as c@example.com in the browser.|Stored the Fable login for Account-3 (c@example.com).|The Fable store now uses Account-3." {
		t.Fatalf("after --allow: exit %d out %q stderr %q", code, out, errb)
	}
	if len(*started) != 1 {
		t.Fatalf("claude's login started %d times", len(*started))
	}
}

// A Fable sign-in with --slot naming an API-key account is refused before
// Claude starts, with the sign-in's own wording (DESIGN A107).
func TestFableSignInOfAnAPIKeySlotOpensNoBrowser(t *testing.T) {
	f := newFableMarkFixture(t)
	if code, _, errb := f.run(t, "add-token", "sk-ant-api03-test-key-4", "--email", "k@example.com", "--slot", "4"); code != 0 {
		t.Fatalf("seeding Account-4: exit %d, stderr %q", code, errb)
	}
	started := countLogins(t, f)
	code, out, errb := f.run(t, "add", "--login", "--lineage", "fable", "--slot", "4")
	want := "Error: Account k@example.com is not stored yet: add it first with tycswap add --login."
	if code != 1 || out != "" || strings.TrimSpace(errb) != want {
		t.Fatalf("exit %d out %q stderr %q", code, out, errb)
	}
	if len(*started) != 0 {
		t.Fatalf("claude's login started %d times", len(*started))
	}
}
