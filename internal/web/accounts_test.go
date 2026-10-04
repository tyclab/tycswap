// Tests for the account lifecycle routes: strategy switch, force switch,
// add / add-token (token never echoed or logged), remove, alias, move, swap,
// the JSON-body rules, and nil-AccountOps 503s.
package web

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/reporting"
)

func TestSwitchStrategy_CallsFacadeForEachStrategy(t *testing.T) {
	for _, st := range Strategies {
		t.Run(st, func(t *testing.T) {
			h := newHarness(t)
			resp := h.postJSON("/api/switch", map[string]any{"strategy": st})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
			}
			body := decodeJSON(t, resp)
			if res, _ := body["result"].(map[string]any); res["strategy"] != st {
				t.Errorf("result %v", body["result"])
			}
			want := []string{"Switch(" + st + ",true,[],<nil>)"}
			if got := h.fa.Calls(); !reflect.DeepEqual(got, want) {
				t.Fatalf("calls %v, want %v", got, want)
			}
		})
	}
}

func TestSwitchStrategy_PassesModels(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/switch", map[string]any{"strategy": "best", "models": []string{"opus", "sonnet"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, []string{"Switch(best,true,[opus sonnet],<nil>)"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestSwitchStrategy_Validation(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		body any
		want int
	}{
		{"empty body", nil, http.StatusBadRequest},
		{"missing strategy", map[string]any{"models": []string{"x"}}, http.StatusBadRequest},
		{"unknown strategy", map[string]any{"strategy": "random"}, http.StatusBadRequest},
		// Regression: soonest-reset is the AUTO-switch ordering only. Passing it
		// to switching.Switch falls through to plain rotation (usage ignored),
		// which once moved a user onto a 96%-used account. It must be refused
		// here, exactly as `tycswap switch --strategy soonest-reset` is.
		{"soonest-reset is auto-only", map[string]any{"strategy": "soonest-reset"}, http.StatusBadRequest},
		{"malformed JSON", "{not json", http.StatusBadRequest},
		{"wrong type", map[string]any{"strategy": 7}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.postJSON("/api/switch", tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
			decodeError(t, resp)
		})
	}
	if len(h.fa.Calls()) != 0 {
		t.Fatalf("facade reached on invalid input: %v", h.fa.Calls())
	}
}

func TestSwitchStrategy_ErrorMapping(t *testing.T) {
	h := newHarness(t)
	h.fa.mu.Lock()
	h.fa.errs = map[string]error{"Switch": cerr.Switch("No eligible account")}
	h.fa.mu.Unlock()
	resp := h.postJSON("/api/switch", map[string]any{"strategy": "next-available"})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if msg := decodeError(t, resp); msg != "No eligible account" {
		t.Fatalf("error %q", msg)
	}
}

func TestSwitchForce_CallsOps(t *testing.T) {
	h := newHarness(t)
	for _, q := range []string{"?force=1", "?force=true", "?force=yes"} {
		resp := h.post("/api/switch/claude:2" + q)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", q, resp.StatusCode)
		}
		body := decodeJSON(t, resp)
		if res, _ := body["result"].(map[string]any); res["forced"] != true {
			t.Errorf("%s: result %v", q, body["result"])
		}
	}
	if got := h.ops.Calls(); !reflect.DeepEqual(got, []string{"SwitchToForce(2,true,true)", "SwitchToForce(2,true,true)", "SwitchToForce(2,true,true)"}) {
		t.Fatalf("ops calls %v", got)
	}
	if len(h.fa.Calls()) != 0 {
		t.Fatalf("plain SwitchTo used for a forced switch: %v", h.fa.Calls())
	}
}

func TestSwitchForce_FalsyFlagUsesFacade(t *testing.T) {
	h := newHarness(t)
	for _, q := range []string{"", "?force=0", "?force=false", "?force="} {
		h.post("/api/switch/claude:2" + q)
	}
	if len(h.ops.Calls()) != 0 {
		t.Fatalf("SwitchToForce called for falsy flag: %v", h.ops.Calls())
	}
	if got := h.fa.Calls(); len(got) != 4 || got[0] != "SwitchTo(2,true)" {
		t.Fatalf("facade calls %v", got)
	}
}

func TestSwitchForce_NilOps503(t *testing.T) {
	h := newHarness(t, withNoAccounts())
	resp := h.post("/api/switch/claude:2?force=1")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
	// Plain switch still works without AccountOps.
	if resp := h.post("/api/switch/claude:2"); resp.StatusCode != http.StatusOK {
		t.Fatalf("plain switch status %d", resp.StatusCode)
	}
}

// TestSwitchConfirmAuthChange_ApprovesThenSwitches: the page asks before a
// switch onto an API-key account and then repeats the call with
// ?confirmAuthChange=1, which the handler turns into the switch layer's
// approval for exactly that account before switching, plain or forced
// (DESIGN A33). Without the flag nothing is approved and the switch layer
// refuses the API-key target on its own.
func TestSwitchConfirmAuthChange_ApprovesThenSwitches(t *testing.T) {
	h := newHarness(t)
	if resp := h.post("/api/switch/claude:3?confirmAuthChange=1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp := h.post("/api/switch/claude:3?confirmAuthChange=1&force=1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("forced: status %d", resp.StatusCode)
	}
	if got := h.ops.Calls(); !reflect.DeepEqual(got, []string{"ApproveAPIKeySwitch(3)", "ApproveAPIKeySwitch(3)", "SwitchToForce(3,true,true)"}) {
		t.Fatalf("ops calls %v", got)
	}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, []string{"SwitchTo(3,true)"}) {
		t.Fatalf("facade calls %v", got)
	}

	plain := newHarness(t)
	for _, q := range []string{"", "?confirmAuthChange=0", "?confirmAuthChange="} {
		plain.post("/api/switch/claude:3" + q)
	}
	if got := plain.ops.Calls(); len(got) != 0 {
		t.Fatalf("approval recorded without the flag: %v", got)
	}
}

// TestSwitchConfirmAuthChange_NilOps503: the approval goes through the
// AccountOps facade, so without it the confirmed switch is unavailable rather
// than attempted unapproved.
func TestSwitchConfirmAuthChange_NilOps503(t *testing.T) {
	h := newHarness(t, withNoAccounts())
	if resp := h.post("/api/switch/claude:3?confirmAuthChange=1"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := h.fa.Calls(); len(got) != 0 {
		t.Fatalf("switched without an approval: %v", got)
	}
}

func TestSwitchForce_ErrorMapping(t *testing.T) {
	h := newHarness(t)
	h.ops.mu.Lock()
	h.ops.errs["SwitchToForce"] = cerr.AccountNotFound("Account 9 not found")
	h.ops.mu.Unlock()
	if resp := h.post("/api/switch/claude:9?force=1"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAddCurrent_CallsFacade(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{nil, map[string]any{}, "{}"} {
		resp := h.postJSON("/api/accounts/add", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
		}
	}
	want := []string{"AddAccount(<nil>,true,<nil>)", "AddAccount(<nil>,true,<nil>)", "AddAccount(<nil>,true,<nil>)"}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v", got)
	}
}

func TestAddCurrent_ErrorMapping(t *testing.T) {
	h := newHarness(t)
	h.fa.mu.Lock()
	h.fa.errs = map[string]error{"AddAccount": cerr.Credential("no current login")}
	h.fa.mu.Unlock()
	resp := h.post("/api/accounts/add")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if msg := decodeError(t, resp); msg != "no current login" {
		t.Fatalf("error %q", msg)
	}
}

func TestAddToken_CallsFacadeWithArgs(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "email": "new@example.com", "slot": "4"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, []string{"AddAccountFromToken(<token>,new@example.com,4,true)"}) {
		t.Fatalf("calls %v", got)
	}
	h.fa.mu.Lock()
	tok := h.fa.lastToken
	h.fa.mu.Unlock()
	if tok != secretSetupToken {
		t.Fatalf("facade received token %q", tok)
	}
	if len(h.ops.Calls()) != 0 {
		t.Fatalf("SetAlias called without alias: %v", h.ops.Calls())
	}
}

func TestAddToken_OptionalFieldsNil(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": "  " + secretSetupToken + "\n"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, []string{"AddAccountFromToken(<token>,<nil>,<nil>,true)"}) {
		t.Fatalf("calls %v", got)
	}
	h.fa.mu.Lock()
	tok := h.fa.lastToken
	h.fa.mu.Unlock()
	if tok != secretSetupToken {
		t.Fatalf("token not trimmed: %q", tok)
	}
}

func TestAddToken_TokenRequired400(t *testing.T) {
	h := newHarness(t)
	// "-" would make the facade read the server's own stdin while holding
	// the mutation lock, so it is refused like an empty token.
	for _, body := range []any{nil, map[string]any{}, map[string]any{"token": ""}, map[string]any{"token": "   "}, map[string]any{"email": "x@y"}, map[string]any{"token": "-"}, map[string]any{"token": " - "}} {
		resp := h.postJSON("/api/accounts/add-token", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %v: status %d, want 400", body, resp.StatusCode)
		}
	}
	if len(h.fa.Calls()) != 0 {
		t.Fatal("facade reached without a token")
	}
}

// A slot that is not a whole number >= 1 is the user's mistake: 400 before
// the facade is reached, not a config error from the store (500).
func TestAddToken_BadSlot400(t *testing.T) {
	h := newHarness(t)
	for _, slot := range []string{"x", "0", "-1", "1.5", "two"} {
		resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "slot": slot})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("slot %q: status %d, want 400", slot, resp.StatusCode)
		}
		decodeError(t, resp)
	}
	if len(h.fa.Calls()) != 0 {
		t.Fatalf("facade reached with a bad slot: %v", h.fa.Calls())
	}
	if resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "slot": " 4 "}); resp.StatusCode != http.StatusOK {
		t.Fatalf("slot with spaces: status %d", resp.StatusCode)
	}
}

func TestAddToken_NeverEchoedOrLogged(t *testing.T) {
	// Success path, failure path, alias path: the token appears in no
	// response body, no state document, no SSE frame and no log line.
	h := newHarness(t)
	st := h.openSSE()
	defer st.close()
	st.nextState(t, timeout)

	var bodies [][]byte
	bodies = append(bodies, readBody(t, h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "email": "a@b", "slot": "7", "alias": "Work"})))
	st.nextState(t, timeout)
	h.fa.mu.Lock()
	h.fa.errs = map[string]error{"AddAccountFromToken": cerr.Validation("Token does not look like a setup-token")}
	h.fa.mu.Unlock()
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("validation status %d", resp.StatusCode)
	}
	bodies = append(bodies, readBody(t, resp))
	bodies = append(bodies, []byte(st.nextState(t, timeout).data))
	bodies = append(bodies, readBody(t, h.get("/api/state")))
	for i, b := range bodies {
		if strings.Contains(string(b), secretSetupToken) || strings.Contains(string(b), "SUPERSECRET") {
			t.Errorf("body %d echoes the token: %s", i, b)
		}
	}
	for _, l := range h.Logs() {
		if strings.Contains(l, secretSetupToken) || strings.Contains(l, "SUPERSECRET") {
			t.Errorf("log line leaks the token: %q", l)
		}
	}
	for _, c := range h.fa.Calls() {
		if strings.Contains(c, "SUPERSECRET") {
			t.Errorf("fake recorded the token: %q", c)
		}
	}
}

func TestAddToken_AliasViaSlot(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "slot": "7", "alias": "Work"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	body := decodeJSON(t, resp)
	res, _ := body["result"].(map[string]any)
	if res["added"] != true || res["number"] != "7" || res["alias"] != "work" {
		t.Errorf("result %v", res)
	}
	if got := h.ops.Calls(); !reflect.DeepEqual(got, []string{"SetAlias(7,Work)"}) {
		t.Fatalf("ops calls %v", got)
	}
}

func TestAddToken_AliasLookupByEmail(t *testing.T) {
	h := newHarness(t)
	h.fa.mu.Lock()
	h.fa.postAddSnap = &reporting.AccountsSnapshot{Accounts: append(sampleSnapshot().Accounts, reporting.AccountSnapshot{Number: "5", Email: "New@Example.com"})}
	h.fa.mu.Unlock()
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "email": "new@example.com", "alias": "fresh"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if got := h.ops.Calls(); !reflect.DeepEqual(got, []string{"SetAlias(5,fresh)"}) {
		t.Fatalf("ops calls %v", got)
	}
	// The lookup must be store-only (non-nil, empty fetch set).
	h.fa.mu.Lock()
	defer h.fa.mu.Unlock()
	found := false
	for _, f := range h.fa.fetchArgs {
		if f != nil && len(f) == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no store-only snapshot taken: %v", h.fa.fetchArgs)
	}
}

// The alias lookup by email reads the merged snapshot's Claude rows only: a
// Codex account with the same email is another CLI's and never aliased.
func TestAddToken_AliasLookupSkipsCodexRows(t *testing.T) {
	h := newHarness(t, withCodex())
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "email": "dana@example.com", "alias": "x"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if got := h.ops.Calls(); len(got) != 0 {
		t.Fatalf("ops calls %v, want none", got)
	}
}

func TestAddToken_AliasWithoutSlotOrEmail404(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "alias": "x"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	// The account was still added.
	if got := h.fa.Calls(); len(got) != 1 || !strings.HasPrefix(got[0], "AddAccountFromToken") {
		t.Fatalf("calls %v", got)
	}
}

func TestAddToken_AliasSetError(t *testing.T) {
	h := newHarness(t)
	h.ops.mu.Lock()
	h.ops.errs["SetAlias"] = cerr.Validation("alias already used")
	h.ops.mu.Unlock()
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "slot": "7", "alias": "dup"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if msg := decodeError(t, resp); !strings.Contains(msg, "alias already used") {
		t.Fatalf("error %q", msg)
	}
}

func TestAddToken_AliasNilOps503_BeforeAdding(t *testing.T) {
	h := newHarness(t, withNoAccounts())
	resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken, "alias": "x"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(h.fa.Calls()) != 0 {
		t.Fatal("account added although alias could not be set")
	}
	// Without an alias the route works without AccountOps.
	if resp := h.postJSON("/api/accounts/add-token", map[string]any{"token": secretSetupToken}); resp.StatusCode != http.StatusOK {
		t.Fatalf("no-alias status %d", resp.StatusCode)
	}
}

func TestRemove_CallsFacade(t *testing.T) {
	h := newHarness(t)
	if resp := h.post("/api/accounts/claude:3/remove"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := h.fa.Calls(); !reflect.DeepEqual(got, []string{"RemoveAccount(3,true)"}) {
		t.Fatalf("calls %v", got)
	}
	h.fa.mu.Lock()
	h.fa.errs = map[string]error{"RemoveAccount": cerr.AccountNotFound("Account 9 not found")}
	h.fa.mu.Unlock()
	if resp := h.post("/api/accounts/claude:9/remove"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("not-found status %d", resp.StatusCode)
	}
}

func TestAlias_SetAndUnset(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/accounts/claude:2/alias", map[string]any{"alias": " Team "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	res, _ := decodeJSON(t, resp)["result"].(map[string]any)
	if res["number"] != "2" || res["alias"] != "team" {
		t.Errorf("set result %v", res)
	}
	for _, body := range []any{map[string]any{"alias": ""}, map[string]any{}, nil} {
		resp = h.postJSON("/api/accounts/claude:2/alias", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unset status %d", resp.StatusCode)
		}
		res, _ = decodeJSON(t, resp)["result"].(map[string]any)
		if res["alias"] != "" || res["number"] != "2" {
			t.Errorf("unset result %v", res)
		}
	}
	want := []string{"SetAlias(2,Team)", "UnsetAlias(2)", "UnsetAlias(2)", "UnsetAlias(2)"}
	if got := h.ops.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func TestAlias_ErrorsAndNil(t *testing.T) {
	h := newHarness(t)
	h.ops.mu.Lock()
	h.ops.errs["SetAlias"] = cerr.Validation("alias must be alphanumeric")
	h.ops.errs["UnsetAlias"] = cerr.AccountNotFound("Account 9 not found")
	h.ops.mu.Unlock()
	if resp := h.postJSON("/api/accounts/claude:2/alias", map[string]any{"alias": "b@d"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("set status %d", resp.StatusCode)
	}
	if resp := h.postJSON("/api/accounts/claude:9/alias", map[string]any{"alias": ""}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unset status %d", resp.StatusCode)
	}
	h2 := newHarness(t, withNoAccounts())
	if resp := h2.postJSON("/api/accounts/claude:2/alias", map[string]any{"alias": "x"}); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil ops status %d", resp.StatusCode)
	}
}

func TestMove_CallsOps(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/accounts/claude:3/move", map[string]any{"slot": "2"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	res, _ := decodeJSON(t, resp)["result"].(map[string]any)
	if res["from"] != "3" || res["to"] != "2" || res["swapped"] != true {
		t.Errorf("result %v", res)
	}
	resp = h.postJSON("/api/accounts/claude:work/move", map[string]any{"slot": "9"})
	res, _ = decodeJSON(t, resp)["result"].(map[string]any)
	if res["swapped"] != false {
		t.Errorf("result %v", res)
	}
	if got := h.ops.Calls(); !reflect.DeepEqual(got, []string{"MoveAccount(3,2)", "MoveAccount(work,9)"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestMove_ValidationAndErrors(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{nil, map[string]any{}, map[string]any{"slot": " "}, "nope"} {
		if resp := h.postJSON("/api/accounts/claude:3/move", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %v: status %d", body, resp.StatusCode)
		}
	}
	if len(h.ops.Calls()) != 0 {
		t.Fatal("MoveAccount reached without slot")
	}
	h.ops.mu.Lock()
	h.ops.errs["MoveAccount"] = cerr.Lock("store is locked")
	h.ops.mu.Unlock()
	if resp := h.postJSON("/api/accounts/claude:3/move", map[string]any{"slot": "2"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("lock status %d", resp.StatusCode)
	}
	h2 := newHarness(t, withNoAccounts())
	if resp := h2.postJSON("/api/accounts/claude:3/move", map[string]any{"slot": "2"}); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil ops status %d", resp.StatusCode)
	}
}

func TestSwap_CallsOps(t *testing.T) {
	h := newHarness(t)
	resp := h.postJSON("/api/accounts/swap", map[string]any{"a": "claude:1", "b": "claude:bob@example.com"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	res, _ := decodeJSON(t, resp)["result"].(map[string]any)
	if res["a"] != "1" || res["b"] != "bob@example.com" {
		t.Errorf("result %v", res)
	}
	if got := h.ops.Calls(); !reflect.DeepEqual(got, []string{"SwapAccounts(1,bob@example.com)"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestSwap_ValidationAndErrors(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{nil, map[string]any{"a": "claude:1"}, map[string]any{"b": "claude:2"}, map[string]any{"a": "", "b": "claude:2"}} {
		if resp := h.postJSON("/api/accounts/swap", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %v: status %d", body, resp.StatusCode)
		}
	}
	h.ops.mu.Lock()
	h.ops.errs["SwapAccounts"] = cerr.Validation("cannot swap an account with itself")
	h.ops.mu.Unlock()
	if resp := h.postJSON("/api/accounts/swap", map[string]any{"a": "claude:1", "b": "claude:1"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
	h2 := newHarness(t, withNoAccounts())
	if resp := h2.postJSON("/api/accounts/swap", map[string]any{"a": "claude:1", "b": "claude:2"}); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil ops status %d", resp.StatusCode)
	}
}

func TestBody_TooLarge413(t *testing.T) {
	h := newHarness(t)
	big := `{"alias":"` + strings.Repeat("a", maxBody+10) + `"}`
	resp := h.postJSON("/api/accounts/claude:2/alias", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}
	if len(h.ops.Calls()) != 0 {
		t.Fatal("ops reached with oversized body")
	}
}

func TestBody_MalformedJSON400_EveryBodyRoute(t *testing.T) {
	h := newHarness(t)
	routes := []struct{ method, path string }{
		{"POST", "/api/switch"}, {"POST", "/api/accounts/add-token"}, {"POST", "/api/accounts/swap"},
		{"POST", "/api/accounts/claude:2/alias"}, {"POST", "/api/accounts/claude:2/move"},
		{"POST", "/api/settings/autoswitch.sevenDayThreshold"}, {"POST", "/api/auto/start"}, {"POST", "/api/auto/threshold"},
	}
	for _, r := range routes {
		resp := h.send(r.method, r.path, "{\"broken")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s: status %d, want 400", r.method, r.path, resp.StatusCode)
		}
	}
	if len(h.fa.Calls())+len(h.ops.Calls())+len(h.set.Calls())+len(h.auto.Calls()) != 0 {
		t.Fatal("a facade was reached with malformed JSON")
	}
}
