package session

import "testing"

func TestCheckCmdShimArgs(t *testing.T) {
	for _, tc := range []struct {
		bin  string
		args []string
		ok   bool
	}{
		{`C:\npm\claude.cmd`, []string{"--resume", "abc-123"}, true},
		{`C:\npm\claude.cmd`, []string{"x & calc"}, false},
		{`C:\npm\claude.CMD`, []string{"a|b"}, false},
		{`C:\npm\claude.bat`, []string{"%PATH%"}, false},
		{`C:\npm\claude.cmd`, []string{"say \"hi\""}, false},
		{`C:\npm\claude.cmd`, []string{"line\nnext"}, false},
		{`C:\bin\claude.exe`, []string{"x & calc"}, true},
		{"/usr/local/bin/claude", []string{"a|b"}, true},
	} {
		err := CheckCmdShimArgs(tc.bin, tc.args)
		if (err == nil) != tc.ok {
			t.Errorf("CheckCmdShimArgs(%q, %q) = %v, want ok=%v", tc.bin, tc.args, err, tc.ok)
		}
	}
}
