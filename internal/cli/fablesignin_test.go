// Tests for the accounts that need a Fable sign-in on status, list and start
// (DESIGN A115).
package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/oauth"
)

// roomInPool is poolAtFableLimit with Fable room on 3, a Fable login in the
// pool: no A112 hint, and 1 and 4 without a Fable login.
var roomInPool = withUsage(poolAtFableLimit, "3", [3]float64{10, 20, 10})

// everyLogin is roomInPool with a Fable login on every account.
var everyLogin = func() fableTickFixture {
	fx := roomInPool
	fx.logins = []string{"1", "2", "3", "4"}
	return fx
}()

const signIn14 = "Accounts 1 and 4 need a Fable sign-in: tycswap fable --login"

// The start notices for the accounts that need a Fable sign-in in the
// fixtures of the Fable start tests.
const (
	signIn14Notice  = "tycswap start: " + signIn14 + "\n"
	signIn34Notice  = "tycswap start: Accounts 3 and 4 need a Fable sign-in: tycswap fable --login\n"
	signIn134Notice = "tycswap start: Accounts 1, 3 and 4 need a Fable sign-in: tycswap fable --login\n"
)

// fableLoginLines is every line of out that names accounts without a Fable
// login: A112's hint or A115's sentence.
func fableLoginLines(out string) string {
	var named []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Fable sign-in") || strings.Contains(line, "Fable room but no Fable login") {
			named = append(named, strings.TrimSpace(line))
		}
	}
	return strings.Join(named, "\n")
}

// jsonNums is nums as decoded JSON: nil for none.
func jsonNums(nums ...float64) any {
	if len(nums) == 0 {
		return nil
	}
	out := []any{}
	for _, n := range nums {
		out = append(out, n)
	}
	return out
}

// status and list name the accounts that need a Fable sign-in where A112's
// hint would be, unless A112's hint is printed; their JSON carries the set
// beside A112's field, each on its own.
func TestFableSignInIsNamedOnStatusAndList(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fx           fableTickFixture
		signIn, room any
		line         string
	}{
		{"a gap, room in the pool", roomInPool, jsonNums(1, 4), nil, signIn14},
		{"no Fable login has room", poolAtFableLimit, jsonNums(1, 4), jsonNums(1), fableRoomHintFor("1")},
		{"no gap", everyLogin, nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.fx.build(t)
			code, out, errb := runSub(t, "status", "--json")
			fable, _ := decodeJSON(t, out)["fable"].(map[string]any)
			if code != 0 || fable == nil {
				t.Fatalf("status --json: code=%d %s %s", code, out, errb)
			}
			if got := fable["withoutLogin"]; !reflect.DeepEqual(got, tc.signIn) {
				t.Errorf("status --json: fable.withoutLogin=%v, want %v", got, tc.signIn)
			}
			if got := fable["roomWithoutLogin"]; !reflect.DeepEqual(got, tc.room) {
				t.Errorf("status --json: fable.roomWithoutLogin=%v, want %v", got, tc.room)
			}
			code, out, errb = runSub(t, "list", "--json")
			payload := decodeJSON(t, out)
			if code != 0 {
				t.Fatalf("list --json: code=%d %s", code, errb)
			}
			if got := payload["fableWithoutLogin"]; !reflect.DeepEqual(got, tc.signIn) {
				t.Errorf("list --json: fableWithoutLogin=%v, want %v", got, tc.signIn)
			}
			if got := payload["fableRoomWithoutLogin"]; !reflect.DeepEqual(got, tc.room) {
				t.Errorf("list --json: fableRoomWithoutLogin=%v, want %v", got, tc.room)
			}
			for _, argv := range [][]string{{"status"}, {"list"}} {
				_, human, _ := runHuman(t, argv...)
				if got := fableLoginLines(human); got != tc.line {
					t.Errorf("%s: lines naming accounts without a Fable login %q, want %q:\n%s", argv[0], got, tc.line, human)
				}
			}
			_, human, _ := runHuman(t, "status")
			lines := strings.Split(human, "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "Fable store: ") && tc.line != "" && (i+1 == len(lines) || lines[i+1] != tc.line) {
					t.Errorf("status: the line under the Fable store line is not %q:\n%s", tc.line, human)
				}
			}
		})
	}
}

// A Fable start names the accounts that need a Fable sign-in once, on stderr
// only, after its other notices, unless they ended in A112's hint; no other
// start names them.
func TestFableStartNamesTheAccountsThatNeedAFableSignIn(t *testing.T) {
	noLog := func(t *testing.T, root string) {
		t.Helper()
		if log := readOrAbsent(t, filepath.Join(root, "tycswap.log")); strings.Contains(log, "Fable sign-in") {
			t.Errorf("tycswap.log names the sign-in:\n%s", log)
		}
	}
	t.Run("room in the pool and a gap", func(t *testing.T) {
		sw, _ := fableStartLogin(t, roomInPool, "1", "2")
		if stderr := startFable(t); stderr != signIn14Notice {
			t.Fatalf("stderr %q, want the sentence once", stderr)
		}
		noLog(t, sw.BackupDir())
	})
	t.Run("A112's notice with its hint", func(t *testing.T) {
		sw, _ := fableStartLogin(t, poolAtFableLimit, "1", "2")
		stderr := startFable(t)
		if strings.Count(stderr, "\n") != 1 || !strings.HasSuffix(stderr, fableRoomHintFor("1")+"\n") {
			t.Fatalf("stderr %q, want A112's notice with its hint only", stderr)
		}
		noLog(t, sw.BackupDir())
	})
	t.Run("A112's notice without a hint", func(t *testing.T) {
		fx := poolAtFableLimit
		fx.logins = []string{"1", "2", "3"}
		sw, _ := fableStartLogin(t, fx, "1", "2")
		if err := autoswitch.Quarantine(fableStatePath(sw), sw.Clk, "1", fableEmail("1"), oauth.ErrInvalidGrant, fableLoginOf("1", farExpiry), func(autoswitch.Event) {}); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(startFable(t), "\n"), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "tycswap start: Fable store: no Fable login has room") ||
			!strings.HasSuffix(lines[0], "starting on the default login.") || lines[1] != "tycswap start: "+signIn14 {
			t.Fatalf("stderr %q, want A112's notice, then the sentence", lines)
		}
		noLog(t, sw.BackupDir())
	})
	t.Run("no gap", func(t *testing.T) {
		fableStartLogin(t, everyLogin, "1", "2")
		if stderr := startFable(t); strings.Contains(stderr, "Fable sign-in") {
			t.Fatalf("stderr %q", stderr)
		}
	})
	t.Run("not a Fable start", func(t *testing.T) {
		fableStartLogin(t, roomInPool, "1", "2")
		code, _, stderr := runCLI(t, []string{"start", "--", "command", "--model", "opus"}, false, false)
		if code != 23 || strings.Contains(stderr, "Fable sign-in") {
			t.Fatalf("start: code=%d stderr=%q", code, stderr)
		}
	})
}
