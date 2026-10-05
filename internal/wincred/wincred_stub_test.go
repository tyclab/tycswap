//go:build !windows

package wincred

import "testing"

func TestNonWindowsStubAlwaysReportsNotFound(t *testing.T) {
	r := New()

	v, found, err := r.Get("claude-code", "account-1-a@x.com")
	if v != "" || found || err != nil {
		t.Fatalf("stub Get = (%q, %v, %v), want (\"\", false, nil)", v, found, err)
	}
	if err := r.Delete("claude-code", "account-1-a@x.com"); err != nil {
		t.Fatalf("stub Delete = %v, want nil (no-op success)", err)
	}
}
