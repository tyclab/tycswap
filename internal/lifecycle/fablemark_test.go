// Tests for the Fable mark (DESIGN A107): what decides whether an account
// runs Fable, `tycswap fable --allow/--disallow`, the sign-in of an account
// whose plan says nothing, and the mark following its identity.
package lifecycle

import (
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
)

// fableMarkStore is fableLifecycleStore plus Account-5 (e@example.com), an
// organisation login whose plan field is empty.
func fableMarkStore(t *testing.T) *store.Store {
	t.Helper()
	s := newStore(t)
	s.Platform = platform.Linux
	seed(t, s, ip(1),
		fableAcct("1", "a@example.com", "max"),
		fableAcct("2", "b@example.com", "max"),
		fableAcct("3", "c@example.com", "pro"),
		acct{num: "4", email: "d@example.com", kind: "api_key", creds: "sk-ant-api03-key-4", config: `{}`},
		fableAcct("5", "e@example.com", ""))
	seedLiveLogin(t, s, "a@example.com", "", "", "uuid-a", planCreds("at-d1", "rt-d1", "max"))
	return s
}

// setRecordKey writes key=value into num's roster record (value nil deletes it).
func setRecordKey(t *testing.T, s *store.Store, num, key string, value any) {
	t.Helper()
	data := readSeq(t, s)
	r := rec(t, data, num)
	if value == nil {
		r.del(key)
	} else {
		r.set(key, value)
	}
	if err := putRecord(data, num, r); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
}

func TestFableEntitlementSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name, num string
		mark      any
		want      store.FableAnswer
	}{
		{"a max plan", "1", nil, store.FableAnswer{Capable: true, By: store.FableByPlan, Plan: "max"}},
		{"a pro plan", "3", nil, store.FableAnswer{By: store.FableByPlan, Plan: "pro"}},
		{"a pro plan, marked", "3", true, store.FableAnswer{By: store.FableByPlan, Plan: "pro"}},
		{"an empty plan", "5", nil, store.FableAnswer{By: store.FableUnknown}},
		{"an empty plan, marked", "5", true, store.FableAnswer{Capable: true, By: store.FableByMark}},
		{"a max plan, marked false by hand", "1", false, store.FableAnswer{By: store.FableByMark, Plan: "max"}},
		{"an API key", "4", nil, store.FableAnswer{By: store.FableAPIKey}},
		{"no such account", "9", nil, store.FableAnswer{By: store.FableUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fableMarkStore(t)
			if tc.mark != nil {
				setRecordKey(t, s, tc.num, store.FableMarkKey, tc.mark)
			}
			got := s.FableEntitlement(readSeq(t, s), tc.num)
			if got != tc.want {
				t.Fatalf("FableEntitlement(%s) = %+v, want %+v", tc.num, got, tc.want)
			}
			if s.FableCapable(tc.num) != got.Capable {
				t.Fatalf("FableCapable(%s) = %v, FableEntitlement says %v", tc.num, s.FableCapable(tc.num), got.Capable)
			}
		})
	}
}

func TestSetFableMarkWritesAndRemovesTheKey(t *testing.T) {
	s := fableMarkStore(t)
	setRecordKey(t, s, "5", "alias", "org")
	if err := SetFableMark(s, "org", true); err != nil {
		t.Fatal(err)
	}
	r := rec(t, readSeq(t, s), "5")
	if v, ok := r.vals[store.FableMarkKey].(bool); !ok || !v || r.str("alias") != "org" {
		t.Fatalf("record after --allow: %+v", r.vals)
	}
	if !s.FableCapable("5") {
		t.Fatal("a marked account cannot run Fable")
	}
	before, _ := os.ReadFile(s.SequenceFile)
	if err := SetFableMark(s, "e@example.com", true); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(s.SequenceFile); string(after) != string(before) {
		t.Fatal("a second --allow rewrote the roster")
	}
	if err := SetFableMark(s, "5", false); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, readSeq(t, s), "5"); r.has(store.FableMarkKey) || s.FableCapable("5") {
		t.Fatalf("record after --disallow: %+v", r.vals)
	}
	before, _ = os.ReadFile(s.SequenceFile)
	if err := SetFableMark(s, "5", false); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(s.SequenceFile); string(after) != string(before) {
		t.Fatal("a second --disallow rewrote the roster")
	}
	setRecordKey(t, s, "1", store.FableMarkKey, false)
	if err := SetFableMark(s, "1", false); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, readSeq(t, s), "1"); r.has(store.FableMarkKey) || !s.FableCapable("1") {
		t.Fatalf("--disallow left a hand-set false: %+v", r.vals)
	}
}

func TestSetFableMarkRefusals(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"9", "Account-9 does not exist"},
		{"3", "Account-3's plan (pro) cannot run Fable; nothing marked."},
		{"4", "Account-4 is an API-key account; a Fable mark is for OAuth logins only."},
	} {
		t.Run(tc.id, func(t *testing.T) {
			s := fableMarkStore(t)
			before := everyFile(t, s)
			err := SetFableMark(s, tc.id, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SetFableMark(%s) = %v, want %q", tc.id, err, tc.want)
			}
			sameFiles(t, "the refusal", before, everyFile(t, s))
		})
	}
}

func TestAddFableLoginOfAnUnknownPlanNamesTheMark(t *testing.T) {
	s := fableMarkStore(t)
	before := everyFile(t, s)
	_, err := AddFableLogin(s, signIn(t, "e@example.com", planCreds("at-f5", "rt-f5", "")), ip(5))
	want := "Account-5's plan does not offer Fable. If it runs Fable, mark it first: tycswap fable --allow 5"
	if err == nil || err.Error() != want {
		t.Fatalf("AddFableLogin = %v, want %q", err, want)
	}
	sameFiles(t, "the refusal", before, everyFile(t, s))
	if err := FablePlanRefusal(s, readSeq(t, s), "5"); err == nil || err.Error() != want {
		t.Fatalf("FablePlanRefusal = %v, want %q", err, want)
	}
	if err := SetFableMark(s, "5", true); err != nil {
		t.Fatal(err)
	}
	if err := FablePlanRefusal(s, readSeq(t, s), "5"); err != nil {
		t.Fatalf("FablePlanRefusal after --allow = %v", err)
	}
	login := planCreds("at-f5", "rt-f5", "")
	if num, err := AddFableLogin(s, signIn(t, "e@example.com", login), ip(5)); err != nil || num != "5" {
		t.Fatalf("AddFableLogin after --allow = %q, %v", num, err)
	}
	if got := fableLoginOf(t, s, "5", "e@example.com"); got != login {
		t.Fatalf("Fable login = %q", got)
	}
}

func TestFablePlanRefusalOfAPlanWithoutFableOffersNoMark(t *testing.T) {
	s := fableMarkStore(t)
	if err := FablePlanRefusal(s, readSeq(t, s), "3"); err == nil || err.Error() != "Account-3's plan does not offer Fable." {
		t.Fatalf("pro: %v", err)
	}
	for _, num := range []string{"1", "4"} {
		if err := FablePlanRefusal(s, readSeq(t, s), num); err != nil {
			t.Errorf("%s: %v", num, err)
		}
	}
}

func TestFableMarkFollowsIdentity(t *testing.T) {
	for _, name := range []string{"re-add", "re-login", "slot re-add", "migrate", "move", "swap", "replace"} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			seed(t, s, nil, switchable("1", "a@example.com"), switchable("2", "b@example.com"))
			seedLiveLogin(t, s, "a@example.com", "", "", "", `{"claudeAiOauth":{"accessToken":"fake","refreshToken":"fake-refresh"}}`)
			if err := SetFableMark(s, "1", true); err != nil {
				t.Fatal(err)
			}
			want := "1"
			var err error
			switch name {
			case "re-add":
				err = AddAccount(s, nil, true, nil)
			case "re-login":
				_, err = AddAccountFrom(s, signIn(t, "a@example.com", `{"claudeAiOauth":{"accessToken":"fake","refreshToken":"fake-refresh"}}`), nil, true, nil)
			case "slot re-add":
				err = AddAccount(s, ip(1), true, nil)
			case "migrate":
				err, want = AddAccount(s, ip(3), true, nil), "3"
			case "move":
				_, _, _, err = MoveAccount(s, "1", "3")
				want = "3"
			case "swap":
				_, _, err = SwapAccounts(s, "1", "2")
				want = "2"
			case "replace":
				seedLiveLogin(t, s, "new@example.com", "", "", "", `{"claudeAiOauth":{"accessToken":"fake"}}`)
				err = AddAccount(s, ip(1), true, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			data := readSeq(t, s)
			for num := range data.Accounts {
				marked := rec(t, data, num).boolVal(store.FableMarkKey)
				if wantMark := num == want && name != "replace"; marked != wantMark {
					t.Errorf("slot %s marked %v, want %v", num, marked, wantMark)
				}
			}
		})
	}
}
