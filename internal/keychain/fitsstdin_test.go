// fitsstdin_test.go — FitsStdin agrees with the command line Set builds.

package keychain

import (
	"strings"
	"testing"
)

func TestFitsStdinMatchesSetsStdinLimit(t *testing.T) {
	overhead := len(`add-generic-password -U -a "a" -s "s" -X ` + "\n")
	largest := (SecurityStdinLineLimit - overhead) / 2 // hex doubles the secret
	if !FitsStdin("s", "a", strings.Repeat("x", largest)) {
		t.Fatalf("a %d-byte secret should fit", largest)
	}
	if FitsStdin("s", "a", strings.Repeat("x", largest+1)) {
		t.Fatalf("a %d-byte secret should not fit", largest+1)
	}
	if !FitsStdin("svc", "acct", "small") {
		t.Fatal("a small secret should fit")
	}
}
