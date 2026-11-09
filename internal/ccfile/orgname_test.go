package ccfile

import "testing"

func TestPlaceholderOrgName(t *testing.T) {
	cases := []struct {
		email, name string
		placeholder bool
	}{
		{"a@x.com", "a@x.com's Organization", true},
		{"a@x.com", "A@X.com's organization", true},
		{"a@x.com", "a@x.com’s Organization", true},
		{"a@x.com", " a@x.com's Organization ", true},
		{"a@x.com", "Acme", false},
		{"a@x.com", "b@x.com's Organization", false},
		{"a@x.com", "", false},
		{"", "'s Organization", false},
	}
	for _, c := range cases {
		if got := PlaceholderOrgName(c.email, c.name); got != c.placeholder {
			t.Errorf("PlaceholderOrgName(%q, %q) = %v, want %v", c.email, c.name, got, c.placeholder)
		}
	}
	if DisplayOrgName("a@x.com", "a@x.com's Organization") != "" || DisplayOrgName("a@x.com", "Acme") != "Acme" {
		t.Fatal("DisplayOrgName keeps real names and drops the placeholder")
	}
}

func TestPlanDisplayName(t *testing.T) {
	if got := PlanDisplayName(`{"oauthAccount":{"emailAddress":"a@x.com","planDisplayName":" Ultra "}}`); got != "Ultra" {
		t.Fatalf("plan = %q", got)
	}
	for _, text := range []string{`{"oauthAccount":{"emailAddress":"a@x.com"}}`, `{}`, `not json`, `{"oauthAccount":{"planDisplayName":7}}`} {
		if got := PlanDisplayName(text); got != "" {
			t.Errorf("PlanDisplayName(%q) = %q, want empty", text, got)
		}
	}
}
