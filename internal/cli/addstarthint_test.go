package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

func TestAddStartLoginHint(t *testing.T) {
	for _, mode := range []string{"current login", "new sign-in"} {
		t.Run(mode, func(t *testing.T) {
			f := newLoginFixture(t)
			args := []string{"add"}
			if mode == "new sign-in" {
				args = append(args, "--login")
			} else {
				f.writeLive(t, "b@example.com", "test-access-b", "test-refresh-b")
			}
			check := func(want string, extra ...string) {
				t.Helper()
				code, out, errText := f.run(t, append(args, extra...)...)
				if code != 0 {
					t.Fatalf("add: %d %s %s", code, out, errText)
				}
				const command = "tycswap start --allow "
				if want == "" {
					if strings.Contains(out+errText, command) {
						t.Errorf("unexpected start login hint: %q", out+errText)
					}
				} else if strings.Count(out+errText, command) != 1 || !strings.Contains(out+errText, command+want) {
					t.Errorf("expected one start login hint for slot %s, got %q", want, out+errText)
				}
			}
			check("") // New account, no marks.
			if code, _, msg := f.run(t, "start", "--allow", "1"); code != 0 {
				t.Fatal(msg)
			}
			check("") // Refresh of the unmarked account.
			if mode == "new sign-in" {
				renewAs(t, "c@example.com")
			} else {
				f.writeLive(t, "c@example.com", "test-access-c", "test-refresh-c")
			}
			check("7", "--slot", "7")
			check("", "--slot", "9") // Moving the same identity to a new slot.
			if code, _, msg := f.run(t, "start", "--allow", "9"); code != 0 {
				t.Fatal(msg)
			}
			check("")
			if code, out, msg := f.run(t, append(args, "--slot", "0")...); code == 0 || strings.Contains(out+msg, "tycswap start --allow ") {
				t.Fatalf("failed add: %d %s %s", code, out, msg)
			}
		})
	}
}

func TestDashboardAddCurrentShowsStartLoginHint(t *testing.T) {
	f := newLoginFixture(t)
	sw, err := newSwitcher(store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.SetStartLogin("1", true); err != nil {
		t.Fatal(err)
	}
	f.writeLive(t, "b@example.com", "test-access-b", "test-refresh-b")
	srv := serveFableDashboard(t, sw)
	for _, refreshed := range []bool{false, true} {
		code, raw := srv.call(http.MethodPost, "/api/accounts/add")
		body := decodeJSON(t, string(raw))
		result, _ := body["result"].(map[string]any)
		message, _ := body["message"].(string) // The dashboard displays this field in its success toast.
		if code != http.StatusOK || result["number"] != "2" || result["refreshed"] != refreshed {
			t.Fatalf("save current login: %d %v", code, body)
		}
		if strings.Contains(message, "tycswap start --allow 2") == refreshed {
			t.Errorf("refreshed %v: dashboard message %q", refreshed, message)
		}
	}
}
