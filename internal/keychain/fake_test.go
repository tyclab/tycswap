package keychain

import (
	"errors"
	"strings"
	"testing"
)

// TestFakeEnforcesTheWrapperLimits: the Fake refuses what Security refuses,
// a name with a control character and a payload over the stdin line, with the
// same errors, so a caller's fallback path is exercised by tests.
func TestFakeEnforcesTheWrapperLimits(t *testing.T) {
	f := NewFake()
	if err := f.Set("svc\n", "acct", "v"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("Set with a control character in the service = %v, want ErrInvalidName", err)
	}
	if _, _, err := f.Get("svc", "acct\r"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("Get with a control character in the account = %v, want ErrInvalidName", err)
	}
	if err := f.Delete("svc", "a\x00b"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("Delete with NUL = %v, want ErrInvalidName", err)
	}
	if f.Exists("svc\x1b", "acct") {
		t.Error("Exists is true for a name the wrapper refuses")
	}

	big := strings.Repeat("x", SecurityStdinLineLimit) // hex doubles it
	err := f.Set("svc", "acct", big)
	if !IsTooLarge(err) || !IsUnusable(err) {
		t.Fatalf("Set of an oversized payload = %v, want a TooLarge KeychainError", err)
	}
	if f.Exists("svc", "acct") {
		t.Error("an oversized payload was stored")
	}
	small := strings.Repeat("x", 100)
	if err := f.Set("svc", "acct", small); err != nil {
		t.Fatalf("Set of a fitting payload = %v", err)
	}
	if v, ok, err := f.Get("svc", "acct"); err != nil || !ok || v != small {
		t.Errorf("Get = %q, %v, %v", v, ok, err)
	}

	// Seed bypasses the limits, as another tool writing through argv does.
	f.Seed("svc", "legacy", big)
	if v, ok, _ := f.Get("svc", "legacy"); !ok || v != big {
		t.Error("Seed did not store the oversized item")
	}
}
