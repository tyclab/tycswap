package printer

import "testing"

func TestAbbreviateUnderNeedsASeparator(t *testing.T) {
	for _, tc := range []struct {
		path, home string
		fold       bool
		sep, want  string
	}{
		{"/home/x", "/home/x", false, "/", "~"},
		{"/home/x/src", "/home/x", false, "/", "~/src"},
		{"/home/xavier", "/home/x", false, "/", "/home/xavier"},
		{"/home/xavier/src", "/home/x", false, "/", "/home/xavier/src"},
		{"/home/x/src", "/home/x/", false, "/", "~/src"},
		{"/other", "/home/x", false, "/", "/other"},
		{`C:\Users\Bob\src`, `C:\Users\bob`, true, `\`, `~\src`},
		{`c:\users\bob`, `C:\Users\Bob`, true, `\`, "~"},
		{`C:\Users\Bobby`, `C:\Users\Bob`, true, `\`, `C:\Users\Bobby`},
		{`C:\Users\Bob\src`, `C:\Users\bob`, false, `\`, `C:\Users\Bob\src`},
	} {
		if got := abbreviateUnder(tc.path, tc.home, tc.fold, tc.sep); got != tc.want {
			t.Errorf("abbreviateUnder(%q, %q, fold=%v) = %q, want %q", tc.path, tc.home, tc.fold, got, tc.want)
		}
	}
}
