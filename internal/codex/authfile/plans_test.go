// plans_test.go — plan-tier naming, normalized to codex-auth v4 semantics.
// Ports claude-swap PR #252 tests/test_codex_plans.py.

package authfile

import "testing"

func TestNormalizePlan(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"legacy team becomes business", "team", "business"},
		{"legacy business becomes enterprise", "business", "enterprise"},
		{"pro passes through", "pro", "pro"},
		{"plus passes through", "plus", "plus"},
		{"enterprise passes through", "enterprise", "enterprise"},
		{"nil becomes empty", nil, ""},
		{"empty stays empty", "", ""},
		{"non-string becomes empty", 3, ""},
		{"json float becomes empty", 3.0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizePlan(c.in); got != c.want {
				t.Fatalf("NormalizePlan(%#v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Applied sequentially, "team" would pass through "business" and land on
// "enterprise" — every Business account mislabelled one tier up.
func TestNormalizePlan_RenameIsASingleLookupNotTwoPasses(t *testing.T) {
	if got := NormalizePlan("team"); got == "enterprise" {
		t.Fatalf("NormalizePlan(team) = %q: renames were chained", got)
	}
}
