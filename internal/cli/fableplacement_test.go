// Tests for the Fable placement rule (DESIGN A112): where an automatically
// placed Fable session belongs when no Fable login has room, and the accounts
// that have Fable room but no Fable login.
package cli

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
)

// poolAtFableLimit is the state observed on 2026-11-24: the default login on
// 1, a start login with Fable room and no Fable login; the Fable store on 2;
// the Fable pool, 2 and 3, at its Fable limit; 4, without a Fable login, at
// its Fable limit too. Tests mark 1 and 2 as start logins.
var poolAtFableLimit = fableTickFixture{def: "1", live: "2", logins: []string{"2", "3"},
	usage: map[string][3]float64{"1": {10, 20, 8}, "2": {10, 20, 100}, "3": {10, 20, 100}, "4": {10, 20, 100}}}

// withUsage is fx with num's cached usage {5h, 7d, Fable} set to u.
func withUsage(fx fableTickFixture, num string, u [3]float64) fableTickFixture {
	fx.usage = maps.Clone(fx.usage)
	fx.usage[num] = u
	return fx
}

// setFablePct sets the Fable window of num's cached usage to pct.
func setFablePct(t *testing.T, sw *core.Switcher, num string, pct float64) {
	t.Helper()
	path := filepath.Join(sw.BackupDir(), "cache", "usage.json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(readOrAbsent(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	row, _ := doc["accounts"].(map[string]any)[num].(map[string]any)
	scoped, _ := row["lastGood"].(map[string]any)["scoped"].([]any)
	scoped[0].(map[string]any)["pct"] = pct
	raw, _ := json.Marshal(doc)
	writeFile(t, path, string(raw))
}

func fableRoomHintFor(nums ...string) string {
	var parts []string
	for _, num := range nums {
		parts = append(parts, "Account-"+num+" has Fable room but no Fable login: tycswap add --login --lineage fable --slot "+num)
	}
	return strings.Join(parts, "; ")
}

// A Fable session that starts while no Fable login has room starts on the
// default login, whose account has Fable room, and says why and where; with a
// pool account that has room, or with no room anywhere, it starts on the
// Fable store with A105's selection as before.
func TestFableStartPlacementByFableRoom(t *testing.T) {
	t.Run("no Fable login has room", func(t *testing.T) {
		sw, rec := fableStartLogin(t, poolAtFableLimit, "1", "2")
		live := storeLive(t, sw)
		stderr := startFable(t)
		now := sw.Clk.Now()
		notice := "Fable store: no Fable login has room (first back: Account-2 at " + oauth.ResetClockString(now.Add(3*time.Hour), now) +
			"); starting on the default login. " + fableRoomHintFor("1")
		if stderr != "tycswap start: "+notice+"\n" {
			t.Fatalf("stderr %q, want %q", stderr, "tycswap start: "+notice+"\n")
		}
		if envValue(rec.env, secureStorageVar) != "" || envValue(rec.env, holdVar) != "" || !reflect.DeepEqual(rec.selections, []string{""}) {
			t.Fatalf("child: on the Fable store %q, hold %q; default selections %q", envValue(rec.env, secureStorageVar), envValue(rec.env, holdVar), rec.selections)
		}
		if storeLive(t, sw) != live || !strings.Contains(defaultLive(t, sw), "at-d1") || holdsIn(t, fableStatePath(sw)) != nil {
			t.Fatalf("Fable store %s, default login %s, Fable holds %v", storeLive(t, sw), defaultLive(t, sw), holdsIn(t, fableStatePath(sw)))
		}
		if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); !strings.Contains(log, "start: "+notice) {
			t.Fatalf("log:\n%s", log)
		}
	})
	for name, fx := range map[string]fableTickFixture{
		"a pool account other than the store's has room": withUsage(poolAtFableLimit, "3", [3]float64{10, 20, 10}),
		"nothing has room": withUsage(poolAtFableLimit, "1", [3]float64{10, 20, 100}),
	} {
		t.Run(name, func(t *testing.T) {
			sw, rec := fableStartLogin(t, fx, "1", "2")
			if stderr := startFable(t); stderr != "" {
				t.Fatalf("stderr %q", stderr)
			}
			token := envValue(rec.env, holdVar)
			if envValue(rec.env, secureStorageVar) != fableStoreDir() || len(rec.selections) != 0 {
				t.Fatalf("child on the Fable store %v, default selections %q", envValue(rec.env, secureStorageVar) == fableStoreDir(), rec.selections)
			}
			if !holdTokenPattern.MatchString(token) || holdsIn(t, fableStatePath(sw))[token] == nil || !strings.Contains(storeLive(t, sw), "at-f2") {
				t.Fatalf("A105: hold %q, Fable holds %v, Fable store %s", token, holdsIn(t, fableStatePath(sw)), storeLive(t, sw))
			}
		})
	}
}

// start --check and start agree where a Fable session goes under the Fable
// placement rule: while no Fable login has room the check names that cause.
func TestStartCheckAgreesWithStartOnFableRoom(t *testing.T) {
	plain := "⚠ Fable session on the default login. Use the Fable store with an account approved for Fable.\n"
	for _, tc := range []struct {
		name      string
		fx        fableTickFixture
		check     string
		toDefault bool
	}{
		{"no Fable login has room", poolAtFableLimit,
			"⚠ Fable session on the default login (no Fable login has room). Use the Fable store with an account approved for Fable.\n", true},
		{"a pool account has room", withUsage(poolAtFableLimit, "3", [3]float64{10, 20, 10}), plain, false},
		{"nothing has room", withUsage(poolAtFableLimit, "1", [3]float64{10, 20, 100}), plain, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rec := fableStartLogin(t, tc.fx, "1", "2")
			testutil.Unsetenv(t, secureStorageVar)
			before := storeTree(t)
			code, out, errb := runCodex(t, statusLine(checkSession, "claude-fable-5-1"), "start", "--check")
			if code != 0 || errb != "" || storeTree(t) != before {
				t.Fatalf("start --check: code=%d stderr=%q, store unchanged %v", code, errb, storeTree(t) == before)
			}
			if out != tc.check {
				t.Fatalf("start --check printed %q, want %q", out, tc.check)
			}
			code, _, stderr := runCLI(t, []string{"start", "--", "command", "--resume", checkSession, "--model", "fable"}, false, false)
			if onFable := envValue(rec.env, secureStorageVar) == fableStoreDir(); code != 23 || onFable == tc.toDefault {
				t.Fatalf("start: code=%d stderr=%q, child on the Fable store %v", code, stderr, onFable)
			}
		})
	}
}

// While no account the Fable store can hold has room, status and list name
// each account that has Fable room but no Fable login, with the command that
// adds it; not a disabled one, one allocated away from Fable, one without
// Fable room or one whose plan has no Fable.
func TestFableRoomWithoutALoginIsNamed(t *testing.T) {
	both := withUsage(poolAtFableLimit, "4", [3]float64{10, 20, 10})
	for _, tc := range []struct {
		name    string
		fx      fableTickFixture
		prepare func(*testing.T, *core.Switcher)
		named   []string
	}{
		{"room and no Fable login", both, nil, []string{"1", "4"}},
		{"disabled", both, func(t *testing.T, sw *core.Switcher) { setRecord(t, sw, "4", "disabled", true) }, []string{"1"}},
		{"allocated away from Fable", both, func(t *testing.T, sw *core.Switcher) { setRecord(t, sw, "4", store.ModelsKey, []string{"opus"}) }, []string{"1"}},
		{"no Fable room", poolAtFableLimit, nil, []string{"1"}},
		{"plan without Fable", both, func(t *testing.T, sw *core.Switcher) { setRecord(t, sw, "4", "subscriptionType", "pro") }, []string{"1"}},
		{"a pool account has room", withUsage(both, "3", [3]float64{10, 20, 10}), nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, _ := tc.fx.build(t)
			if tc.prepare != nil {
				tc.prepare(t, sw)
			}
			var want any
			if len(tc.named) > 0 {
				nums := []any{}
				for _, num := range tc.named {
					n, _ := strconv.Atoi(num)
					nums = append(nums, float64(n))
				}
				want = nums
			}
			code, out, errb := runSub(t, "status", "--json")
			fable, _ := decodeJSON(t, out)["fable"].(map[string]any)
			if got := fable["roomWithoutLogin"]; code != 0 || !reflect.DeepEqual(got, want) {
				t.Fatalf("status --json: code=%d fable.roomWithoutLogin=%v, want %v (%s)", code, got, want, errb)
			}
			code, out, errb = runSub(t, "list", "--json")
			if got := decodeJSON(t, out)["fableRoomWithoutLogin"]; code != 0 || !reflect.DeepEqual(got, want) {
				t.Fatalf("list --json: code=%d fableRoomWithoutLogin=%v, want %v (%s)", code, got, want, errb)
			}
			hint := fableRoomHintFor(tc.named...)
			for _, argv := range [][]string{{"status"}, {"list"}} {
				_, human, _ := runHuman(t, argv...)
				lines := strings.Split(human, "\n")
				found := false
				for _, line := range lines {
					found = found || hint != "" && strings.TrimSpace(line) == hint
				}
				if hint != "" && !found || hint == "" && strings.Contains(human, "has Fable room but no Fable login") {
					t.Fatalf("%s: want the line %q:\n%s", argv[0], hint, human)
				}
			}
		})
	}
}

// fableRoomWarnings is every WARNING tycswap.log holds that names an account
// with Fable room but no Fable login.
func fableRoomWarnings(t *testing.T, sw *core.Switcher) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")), "\n") {
		if rec, ok := logging.ParseLine(line); ok && rec.Level == logging.LevelWarning && strings.Contains(rec.Message, "has Fable room but no Fable login") {
			out = append(out, rec.Message)
		}
	}
	return out
}

// The Fable tick writes one WARNING when the set of accounts with Fable room
// but no Fable login changes, not every tick; a dry run writes none.
func TestFableTickWarnsOnceAboutFableRoomWithoutALogin(t *testing.T) {
	sw, _ := poolAtFableLimit.build(t)
	runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	if got := fableRoomWarnings(t, sw); len(got) != 0 {
		t.Fatalf("a dry run warned: %q", got)
	}
	first := "Fable store: no Fable login has room; " + fableRoomHintFor("1")
	for range 2 {
		runCodex(t, "", "auto", "--once", "--json")
	}
	if got := fableRoomWarnings(t, sw); !reflect.DeepEqual(got, []string{first}) {
		t.Fatalf("warnings %q, want %q once", got, first)
	}
	setFablePct(t, sw, "4", 10)
	for range 2 {
		runCodex(t, "", "auto", "--once", "--json")
	}
	second := "Fable store: no Fable login has room; " + fableRoomHintFor("1", "4")
	if got := fableRoomWarnings(t, sw); !reflect.DeepEqual(got, []string{first, second}) {
		t.Fatalf("warnings %q, want %q then %q", got, first, second)
	}
}
