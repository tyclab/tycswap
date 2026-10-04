package web

import (
	"net/http"
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
