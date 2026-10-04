package web

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
)

// A new login becomes the active account and the answer names it.
func TestAddCurrentLogin_NewAccount(t *testing.T) {
	h := newHarness(t)
	added := &reporting.AccountsSnapshot{ActiveNumber: "4", Accounts: append(sampleSnapshot().Accounts,
		reporting.AccountSnapshot{Number: "4", Email: "dave@example.com", Kind: "oauth", IsActive: true})}
	for i := range added.Accounts[:3] {
		added.Accounts[i].IsActive = false
	}
	h.fa.mu.Lock()
	h.fa.afterAdd = added
	h.fa.mu.Unlock()
	resp := h.post("/api/accounts/add")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	res := decodeJSON(t, resp)["result"].(map[string]any)
	if res["number"] != "4" || res["email"] != "dave@example.com" || res["refreshed"] != false {
		t.Errorf("result %v", res)
	}
}

// A login that is one of the accounts already is refreshed, and says so.
func TestAddCurrentLogin_Refreshed(t *testing.T) {
	h := newHarness(t)
	res, err := h.s.AddCurrentLogin()
	if err != nil {
		t.Fatal(err)
	}
	var active reporting.AccountSnapshot
	for _, a := range sampleSnapshot().Accounts {
		if a.IsActive {
			active = a
		}
	}
	if !res.Refreshed || res.Number != active.Number || res.Email != active.Email {
		t.Errorf("result %+v, want the active account %s %s refreshed", res, active.Number, active.Email)
	}
}

// Without a live login there is nothing to add: the dashboard says what the
// tray says, and nothing is tried.
func TestAddCurrentLogin_NoLogin(t *testing.T) {
	h := newHarness(t)
	h.setLogin("", false)
	resp := h.post("/api/accounts/add")
	body := string(readBody(t, resp))
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "Claude Code has no subscription login on this computer.") {
		t.Errorf("status %d: %s", resp.StatusCode, body)
	}
	for _, c := range h.fa.Calls() {
		if strings.HasPrefix(c, "AddAccount(") {
			t.Errorf("AddAccount ran without a login: %v", h.fa.Calls())
		}
	}
	if _, err := h.s.AddCurrentLogin(); !errors.Is(err, ErrNoLogin) {
		t.Errorf("AddCurrentLogin = %v, want ErrNoLogin", err)
	}
}
