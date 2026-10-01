package storenames

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidEmail(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"a@example.com", true},
		{"first.last+tag@sub.example.co", true},
		{"a/../../../x", false},
		{"a/../../../x@example.com", false},
		{`a\..\x@example.com`, false},
		{"a@example.com\n", false},
		{"a b@example.com", false},
		{"", false},
		{strings.Repeat("a", 250) + "@x.com", false},
	} {
		if got := ValidEmail(tc.in); got != tc.want {
			t.Errorf("ValidEmail(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestNamesNeverLeaveTheirDirectory: whatever the email holds, the encoded
// names are single path components.
func TestNamesNeverLeaveTheirDirectory(t *testing.T) {
	for _, email := range []string{"a/../../../x", `..\..\x`, "a@example.com", "..", "/etc/passwd", "é@x.com"} {
		for _, name := range []string{ConfigFile("1", email), CredsFile("1", email), CredsPrevFile("1", email)} {
			if strings.ContainsAny(name, `/\:`) || filepath.Base(name) != name {
				t.Errorf("%q -> %q is not a single safe component", email, name)
			}
		}
	}
	if EmailKey("a@x.com") == EmailKey("a@x.co") {
		t.Error("EmailKey is not injective")
	}
	if got := ConfigFile("3", "a@example.com"); got != ".claude-config-3-YUBleGFtcGxlLmNvbQ.json" {
		t.Errorf("ConfigFile = %q", got)
	}
}
