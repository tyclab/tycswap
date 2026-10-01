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
		{"a'b@example.com", true},         // apostrophe local part
		{"jörg@example.com", true},        // non-ASCII letter
		{"a@xn--bcher-kva.example", true}, // IDN domain
		{"a@example.c0m", true},           // digit in the TLD
		{"a@b", true},                     // no dot in the domain
		{"", false},
		{"@example.com", false},   // empty local part
		{"a@", false},             // empty domain
		{"a@@example.com", false}, // two @
		{"a@b@example.com", false},
		{"a/../../../x", false},
		{"a/../../../x@example.com", false},
		{`a\..\x@example.com`, false},
		{"a@example.com\n", false},
		{"a b@example.com", false},
		{"a\u00a0b@example.com", false},  // no-break space
		{"a\x00b@example.com", false},    // NUL
		{"a\x1b[2Jb@example.com", false}, // ESC
		{"a\u0085b@example.com", false},  // C1
		{"a<b@example.com", false},
		{"a>b@example.com", false},
		{"a:b@example.com", false},
		{`a"b@example.com`, false},
		{"a|b@example.com", false},
		{"a?b@example.com", false},
		{"a*b@example.com", false},
		{"\xffa@example.com", false}, // invalid UTF-8
		{strings.Repeat("a", 250) + "@x.com", false},
	} {
		if got := ValidEmail(tc.in); got != tc.want {
			t.Errorf("ValidEmail(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestValidEmailAdmitsOnlySafeBytes: every ASCII byte the rule lets into an
// email is one that is inert in a file name, which is what makes a validated
// email a single path component without any encoding.
func TestValidEmailAdmitsOnlySafeBytes(t *testing.T) {
	for b := 0; b < 128; b++ {
		c := string([]byte{byte(b)})
		if !ValidEmail("a"+c+"b@example.com") && !ValidEmail("ab@ex"+c+"ample.com") {
			continue
		}
		if b <= 0x20 || b == 0x7f || strings.ContainsAny(c, `/\:*?"<>|`) {
			t.Errorf("ValidEmail admits byte %#x, which is not safe in a file name", b)
		}
	}
}

func TestStrictEmail(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"a@example.com", true},
		{"first.last+tag%x@sub.example.co", true},
		{"a'b@example.com", false},
		{"jörg@example.com", false},
		{"a@b", false},
		{"a@example.c0m", false},
		{"a@example.com\n", false},
		{"a b@example.com", false},
		{"a/../../../x", false},
		{"", false},
		{strings.Repeat("a", 250) + "@x.com", false},
	} {
		if got := StrictEmail(tc.in); got != tc.want {
			t.Errorf("StrictEmail(%q) = %v, want %v", tc.in, got, tc.want)
		}
		if got := StrictEmail(tc.in); got && !ValidEmail(tc.in) {
			t.Errorf("StrictEmail accepts %q but ValidEmail refuses it", tc.in)
		}
	}
}

// TestNamesAreTheRawEmailScheme: the names are the email joined as it is, and
// for a validated email each is a single path component.
func TestNamesAreTheRawEmailScheme(t *testing.T) {
	for want, got := range map[string]string{
		".claude-config-3-a@example.com.json": ConfigFile("3", "a@example.com"),
		".creds-3-a@example.com.enc":          CredsFile("3", "a@example.com"),
		".creds-3-a@example.com.enc.prev":     CredsPrevFile("3", "a@example.com"),
		".creds-None-a@example.com.enc":       CredsFile("None", "a@example.com"),
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	for _, email := range []string{"a@example.com", "first.last+tag%x-y_z@sub.example.co", "a'b@example.com", "jörg@example.com", strings.Repeat("a", 240) + "@example.com"} {
		if !ValidEmail(email) {
			t.Fatalf("%q should be a valid email", email)
		}
		for _, name := range []string{ConfigFile("1", email), CredsFile("1", email), CredsPrevFile("1", email)} {
			if filepath.Base(name) != name || filepath.Clean(name) != name || strings.ContainsAny(name, `/\`) {
				t.Errorf("%q -> %q is not a single path component", email, name)
			}
		}
	}
}
