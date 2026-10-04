// The TUI's side of an API-key account with a base URL (DESIGN A46): the
// add-token modal's base URL field, the call it makes, and the account rows
// that name the endpoint's host.
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/usage"
)

// baseURLFacade is the fake facade with the optional add-token-with-URL
// method *core.Switcher provides.
type baseURLFacade struct {
	*fakeFacade
	calls []string // "token|baseURL"
}

func (f *baseURLFacade) AddAccountFromTokenWithBaseURL(token, baseURL string, email, slotArg *string, assumeYes bool) error {
	f.calls = append(f.calls, token+"|"+baseURL)
	return nil
}

func keyTo(a *addTokenModal, m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "tab", "enter", "esc", "left", "right", "backspace":
			msg = tea.KeyMsg{Type: map[string]tea.KeyType{"tab": tea.KeyTab, "enter": tea.KeyEnter, "esc": tea.KeyEsc,
				"left": tea.KeyLeft, "right": tea.KeyRight, "backspace": tea.KeyBackspace}[k]}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		a.update(m, msg)
	}
}

// TestAddTokenModalBaseURLField: the fourth field takes the base URL, the
// focus ring has the two buttons after it, and the form carries the trimmed
// URL.
func TestAddTokenModalBaseURLField(t *testing.T) {
	m := newTestModel(&fakeFacade{})
	var got *tokenForm
	a := &addTokenModal{onDone: func(_ *Model, form *tokenForm) tea.Cmd { got = form; return nil }}
	m.pushScreen(a)
	if !strings.Contains(stripANSI(a.view(m)), "base URL (optional)") {
		t.Error("the modal does not show the base URL field")
	}
	keyTo(a, m, "sk-gw-1", "tab", "tab", "tab", " https://gw.example.com ")
	if a.focus != 3 || a.baseURL != " https://gw.example.com " {
		t.Fatalf("focus %d, baseURL %q", a.focus, a.baseURL)
	}
	if !strings.Contains(stripANSI(a.view(m)), "https://gw.example.com") {
		t.Error("the modal does not show the typed base URL")
	}
	keyTo(a, m, "tab", "tab", "tab")
	if a.focus != 0 {
		t.Errorf("focus after the ring = %d, want back at the token", a.focus)
	}
	keyTo(a, m, "tab", "tab", "tab", "tab", "right")
	if a.focus != addTokenCancelBtn {
		t.Errorf("right from Add = %d, want Cancel", a.focus)
	}
	keyTo(a, m, "left", "enter")
	if got == nil || got.Token != "sk-gw-1" || got.BaseURL != "https://gw.example.com" {
		t.Fatalf("form = %+v", got)
	}
}

// TestRunTokenFormWithBaseURL: a form with a base URL goes to the optional
// method; without one, to the frozen AddAccountFromToken; a facade without
// the method reports it instead of dropping the URL.
func TestRunTokenFormWithBaseURL(t *testing.T) {
	f := &baseURLFacade{fakeFacade: &fakeFacade{}}
	m := newModel(f, "dashboard")
	msg := runCmd(m.runTokenForm(&tokenForm{Token: "sk-gw-1", BaseURL: "https://gw.example.com"}))
	if done, ok := msg.(actionDoneMsg); !ok || !done.result.OK {
		t.Fatalf("action = %#v", msg)
	}
	if len(f.calls) != 1 || f.calls[0] != "sk-gw-1|https://gw.example.com" || len(f.addTokCalls) != 0 {
		t.Errorf("calls: with URL %v, frozen %v", f.calls, f.addTokCalls)
	}
	m.busy = false
	runCmd(m.runTokenForm(&tokenForm{Token: "sk-ant-api03-x"}))
	if len(f.addTokCalls) != 1 || len(f.calls) != 1 {
		t.Errorf("a form without a URL: with URL %v, frozen %v", f.calls, f.addTokCalls)
	}

	plain := newTestModel(&fakeFacade{})
	msg = runCmd(plain.runTokenForm(&tokenForm{Token: "k", BaseURL: "https://gw.example.com"}))
	if done, ok := msg.(actionDoneMsg); !ok || done.result.OK || !strings.Contains(done.result.Message, "base URL") {
		t.Errorf("facade without the method: %#v", msg)
	}
}

// TestAccountRowsNameTheEndpoint: the card, the per-row line and the monitor
// table's span row of an API-key account with a base URL say where its
// requests go; one without a URL does not.
func TestAccountRowsNameTheEndpoint(t *testing.T) {
	acc := reporting.AccountSnapshot{Number: "2", Email: "gw@x.com", Kind: "api_key", BaseURL: "https://gw.example.com/anthropic",
		Usage: usage.UsageEntry{Sentinel: jsonout.UsageAPIKey}, Switchable: true}
	want := "API key (no quota) → gw.example.com"
	if got := accountCardText(acc, 100, nil, 0).plain(); !strings.Contains(got, want) {
		t.Errorf("card = %q", got)
	}
	if got := miniAccountText(acc, 200, 0).plain(); !strings.Contains(got, want) {
		t.Errorf("mini line = %q", got)
	}
	if row := monitorRow(acc); row.Span != want {
		t.Errorf("monitor span = %q", row.Span)
	}
	acc.BaseURL = ""
	if got := accountCardText(acc, 100, nil, 0).plain(); strings.Contains(got, "→") {
		t.Errorf("a key without a URL names an endpoint: %q", got)
	}
}
