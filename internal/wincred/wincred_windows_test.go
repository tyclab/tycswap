package wincred

import (
	"errors"
	"testing"
)

// TestRealRefusesInTests: inside a test binary the real Credential Manager
// calls answer errInTests before reaching it, so Get and Delete do too. A
// wrapper that answers otherwise stops the test before Get and Delete reach
// the real claude-code target.
func TestRealRefusesInTests(t *testing.T) {
	if _, _, _, err := credRead("wincred-test-target"); !errors.Is(err, errInTests) {
		t.Fatalf("credRead = %v, want errInTests", err)
	}
	if err := credDelete("wincred-test-target"); !errors.Is(err, errInTests) {
		t.Fatalf("credDelete = %v, want errInTests", err)
	}
	if _, _, err := New().Get("claude-code", "account-1-a@x.com"); !errors.Is(err, errInTests) {
		t.Errorf("Get = %v, want errInTests", err)
	}
	if err := New().Delete("claude-code", "account-1-a@x.com"); !errors.Is(err, errInTests) {
		t.Errorf("Delete = %v, want errInTests", err)
	}
}

// credEntry is a Credential Manager entry: its secret and its UserName
// field. err makes reading it fail.
type credEntry struct {
	value, user string
	err         error
}

// fakeCredManager puts entries, keyed by target name, behind credRead and
// credDelete until the test ends. It returns the targets deleted, in order.
func fakeCredManager(t *testing.T, entries map[string]credEntry) *[]string {
	t.Helper()
	prevRead, prevDelete := credRead, credDelete
	t.Cleanup(func() { credRead, credDelete = prevRead, prevDelete })
	deleted := new([]string)
	credRead = func(target string) (string, string, bool, error) {
		e, ok := entries[target]
		return e.value, e.user, ok && e.err == nil, e.err
	}
	credDelete = func(target string) error {
		*deleted = append(*deleted, target)
		delete(entries, target)
		return nil
	}
	return deleted
}

// TestRealResolvesTargetNames: Get takes the plain service target only when
// its UserName is the account, and otherwise the compound "account@service"
// target; Delete removes the target Get resolves.
func TestRealResolvesTargetNames(t *testing.T) {
	const service, account, compound = "claude-code", "account-2-b@x.com", "account-2-b@x.com@claude-code"
	otherUser := credEntry{value: "secret-1", user: "account-1-a@x.com"}
	for _, tc := range []struct {
		name    string
		entries map[string]credEntry
		want    string // Get's value; "" is not found
		deletes string
	}{
		{"plain target, same user", map[string]credEntry{service: {value: "plain", user: account}, compound: {value: "compound", user: account}}, "plain", service},
		{"plain target, other user", map[string]credEntry{service: otherUser, compound: {value: "compound", user: account}}, "compound", compound},
		{"compound target only", map[string]credEntry{compound: {value: "compound", user: account}}, "compound", compound},
		{"neither", map[string]credEntry{service: otherUser}, "", compound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted := fakeCredManager(t, tc.entries)
			v, found, err := New().Get(service, account)
			if err != nil || found != (tc.want != "") || v != tc.want {
				t.Errorf("Get = (%q, %v, %v), want (%q, %v, nil)", v, found, err, tc.want, tc.want != "")
			}
			if err := New().Delete(service, account); err != nil {
				t.Errorf("Delete = %v", err)
			}
			if len(*deleted) != 1 || (*deleted)[0] != tc.deletes {
				t.Errorf("Delete removed %q, want [%s]", *deleted, tc.deletes)
			}
		})
	}
}

// TestRealSurfacesReadErrors: a failed read is Get's error, and Delete's
// when it is the plain target's, with nothing deleted.
func TestRealSurfacesReadErrors(t *testing.T) {
	const service, account, compound = "claude-code", "account-2-b@x.com", "account-2-b@x.com@claude-code"
	otherUser := credEntry{value: "secret-1", user: "account-1-a@x.com"}
	denied := errors.New("CredReadW: access denied")
	deleted := fakeCredManager(t, map[string]credEntry{service: {err: denied}})
	if _, _, err := New().Get(service, account); !errors.Is(err, denied) {
		t.Errorf("Get with the plain target unreadable = %v, want %v", err, denied)
	}
	if err := New().Delete(service, account); !errors.Is(err, denied) || len(*deleted) != 0 {
		t.Errorf("Delete with the plain target unreadable = %v, removed %q; want %v, nothing removed", err, *deleted, denied)
	}

	fakeCredManager(t, map[string]credEntry{service: otherUser, compound: {err: denied}})
	if _, _, err := New().Get(service, account); !errors.Is(err, denied) {
		t.Errorf("Get with the compound target unreadable = %v, want %v", err, denied)
	}
}
