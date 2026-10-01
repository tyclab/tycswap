package termsafe

import "testing"

func TestStrip(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Acme Corp", "Acme Corp"},
		{"Ünïcødé ✓", "Ünïcødé ✓"},
		{"\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"\x1b]0;title\x07x", "]0;titlex"},
		{"a\u009b31mb", "a31mb"},
		{"a\x9bb", "a�b"},
		{"line\r\nnext\x00\x7f", "linenext"},
		{"tab\there", "tabhere"},
	} {
		if got := Strip(tc.in); got != tc.want {
			t.Errorf("Strip(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if HasControl("plain") || !HasControl("x\x1b") {
		t.Error("HasControl")
	}
}
