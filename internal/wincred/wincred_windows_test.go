package wincred

import (
	"errors"
	"testing"
)

// TestRealRefusesInTests: inside a test binary the real client answers
// errInTests before calling into the Credential Manager.
func TestRealRefusesInTests(t *testing.T) {
	if _, _, err := New().Get("claude-code", "account-1-a@x.com"); !errors.Is(err, errInTests) {
		t.Errorf("Get = %v, want errInTests", err)
	}
	if err := New().Delete("claude-code", "account-1-a@x.com"); !errors.Is(err, errInTests) {
		t.Errorf("Delete = %v, want errInTests", err)
	}
}
