// workspaces_test.go — workspace-name refresh, mostly a test of when we DON'T
// make a request. Ports claude-swap PR #252 tests/test_codex_workspaces.py.

package usagecache

import (
	"context"
	"errors"
	"testing"

	"git.dpemmons.com/dpemmons/cswap/internal/codex/api"
	"git.dpemmons.com/dpemmons/cswap/internal/codex/authfile"
	"git.dpemmons.com/dpemmons/cswap/internal/codex/store"
)

const wsUser = "user-a"

var (
	wsKey1        = authfile.AccountKey(wsUser, "team-1")
	wsKey2        = authfile.AccountKey(wsUser, "team-2")
	wsKeyPersonal = authfile.AccountKey(wsUser, "personal-1")
	wsKeyOther    = authfile.AccountKey("user-b", "team-9")
)

// seed is _seed: a slot plus a snapshot whose tokens name its account id.
func seed(t *testing.T, st *store.Store, key, plan, name string) {
	t.Helper()
	mustUpsert(t, st, key, store.Upsert{Email: "a@x", Plan: plan, WorkspaceName: name})
	mustSnapshot(t, st, key, accountID(key))
}

func names(st *store.Store) map[string]string {
	out := map[string]string{}
	for _, s := range st.Slots() {
		out[s.AccountKey] = s.WorkspaceName
	}
	return out
}

func (e *env) refreshNames(ac *accountsCounter) int {
	return RefreshWorkspaceNames(context.Background(), e.open(), ac.client(), e.payloadFor)
}

// ---- when NOT to ask ---------------------------------------------------

// A single personal account is the common case; it must cost nothing.
func TestLoneAccountNeverTriggersARequest(t *testing.T) {
	e := newEnv(t)
	seed(t, e.open(), wsKeyPersonal, "plus", "")
	ac := &accountsCounter{}
	e.refreshNames(ac)
	if len(ac.calls) != 0 {
		t.Fatalf("calls = %v, want none", ac.calls)
	}
}

// A lone unnamed workspace plan is still a scope of one: no request.
func TestLoneWorkspaceAccountNeverTriggersARequest(t *testing.T) {
	e := newEnv(t)
	seed(t, e.open(), wsKey1, "business", "")
	ac := &accountsCounter{}
	e.refreshNames(ac)
	if len(ac.calls) != 0 {
		t.Fatalf("calls = %v, want none", ac.calls)
	}
}

func TestScopeWithNoWorkspacePlanNeverAsks(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	seed(t, st, wsKeyPersonal, "plus", "")
	seed(t, st, authfile.AccountKey(wsUser, "personal-2"), "pro", "")
	ac := &accountsCounter{}
	e.refreshNames(ac)
	if len(ac.calls) != 0 {
		t.Fatalf("calls = %v, want none", ac.calls)
	}
}

func TestScopeWhoseNamesAreAllKnownNeverAsks(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	seed(t, st, wsKey1, "business", "Alpha")
	seed(t, st, wsKey2, "business", "Beta")
	ac := &accountsCounter{}
	e.refreshNames(ac)
	if len(ac.calls) != 0 {
		t.Fatalf("calls = %v, want none", ac.calls)
	}
}

func TestAPIKeyAccountIsNotPartOfAnyScope(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	mustUpsert(t, st, wsKey1, store.Upsert{Email: "a@x", Plan: "business", AuthMode: "apikey"})
	mustUpsert(t, st, wsKey2, store.Upsert{Email: "a@x", Plan: "business", AuthMode: "apikey"})
	mustSnapshot(t, st, wsKey1, "team-1")
	ac := &accountsCounter{}
	e.refreshNames(ac)
	if len(ac.calls) != 0 {
		t.Fatalf("calls = %v, want none", ac.calls)
	}
}

func TestScopePredicateMatchesTheDocumentedRules(t *testing.T) {
	slot := func(key, plan, name string) store.Slot {
		return store.Slot{Number: "1", AccountKey: key, Plan: plan, WorkspaceName: name}
	}
	cases := []struct {
		name  string
		scope []store.Slot
		want  bool
	}{
		{"one record", []store.Slot{slot(wsKey1, "business", "")}, false},
		{"no workspace plan", []store.Slot{slot(wsKey1, "plus", ""), slot(wsKey2, "pro", "")}, false},
		{"all named", []store.Slot{slot(wsKey1, "business", "A"), slot(wsKey2, "business", "B")}, false},
		{"one unnamed", []store.Slot{slot(wsKey1, "business", ""), slot(wsKey2, "business", "B")}, true},
		{"personal rides along", []store.Slot{slot(wsKeyPersonal, "plus", ""), slot(wsKey1, "business", "")}, true},
		{"enterprise", []store.Slot{slot(wsKey1, "enterprise", ""), slot(wsKey2, "plus", "")}, true},
		{"edu", []store.Slot{slot(wsKey1, "edu", ""), slot(wsKey2, "plus", "")}, true},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScopeNeedsRefresh(tc.scope); got != tc.want {
				t.Fatalf("ScopeNeedsRefresh = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---- when to ask, and what to do with the answer -----------------------

func TestOneRequestPerScopeFillsEveryWorkspaceInIt(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	seed(t, st, wsKey1, "business", "")
	seed(t, st, wsKey2, "business", "")
	ac := &accountsCounter{reply: []api.Workspace{
		{AccountID: "team-1", Name: "Workspace Alpha"},
		{AccountID: "team-2", Name: "Workspace Beta"},
	}}
	changed := e.refreshNames(ac)
	if len(ac.calls) != 1 {
		t.Fatalf("calls = %v, want one request answering for both", ac.calls)
	}
	if ac.calls[0] != (call{"at-team-1", "team-1"}) {
		t.Fatalf("asked with %v, want the first slot's credentials", ac.calls[0])
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2", changed)
	}
	got := names(e.open())
	if got[wsKey1] != "Workspace Alpha" || got[wsKey2] != "Workspace Beta" {
		t.Fatalf("names = %v", got)
	}
}

// The server is authoritative; a renamed workspace should follow.
func TestReturnedNameOverwritesAnOlderOne(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	seed(t, st, wsKey1, "business", "")
	seed(t, st, wsKey2, "business", "Old Name")
	ac := &accountsCounter{reply: []api.Workspace{{AccountID: "team-1", Name: "Alpha"}, {AccountID: "team-2", Name: "Sandbox"}}}
	e.refreshNames(ac)
	if got := names(e.open())[wsKey2]; got != "Sandbox" {
		t.Fatalf("name = %q, want Sandbox", got)
	}
}

// Leaving a stale name for a workspace the user has lost access to is worse
// than showing none.
func TestInScopeWorkspaceNotReturnedIsCleared(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	seed(t, st, wsKey1, "business", "")
	seed(t, st, wsKey2, "business", "Gone")
	ac := &accountsCounter{reply: []api.Workspace{{AccountID: "team-1", Name: "Alpha"}}}
	changed := e.refreshNames(ac)
	got := names(e.open())
	if got[wsKey2] != "" || got[wsKey1] != "Alpha" {
		t.Fatalf("names = %v, want team-2 cleared, team-1 Alpha", got)
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2", changed)
	}
}

// A personal record never had a workspace name to lose.
func TestPersonalRecordInScopeIsNeverCleared(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	seed(t, st, wsKeyPersonal, "plus", "")
	seed(t, st, wsKey1, "business", "")
	ac := &accountsCounter{reply: []api.Workspace{{AccountID: "team-1", Name: "Alpha"}}}
	changed := e.refreshNames(ac)
	got := names(e.open())
	if got[wsKeyPersonal] != "" || got[wsKey1] != "Alpha" {
		t.Fatalf("names = %v", got)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
}

// Any failure, or an empty answer, leaves every stored name untouched.
func TestFailedRequestLeavesEveryStoredNameUntouched(t *testing.T) {
	cases := []struct {
		name  string
		reply []api.Workspace
		err   error
	}{
		{"empty answer", nil, nil},
		{"error", []api.Workspace{{AccountID: "team-2", Name: "X"}}, errors.New("http 500")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			st := e.open()
			seed(t, st, wsKey1, "business", "Alpha")
			seed(t, st, wsKey2, "business", "")
			ac := &accountsCounter{reply: tc.reply, err: tc.err}
			changed := e.refreshNames(ac)
			if len(ac.calls) != 1 {
				t.Fatalf("calls = %v, want 1", ac.calls)
			}
			if changed != 0 {
				t.Fatalf("changed = %d, want 0", changed)
			}
			got := names(e.open())
			if got[wsKey1] != "Alpha" || got[wsKey2] != "" {
				t.Fatalf("names = %v, want untouched", got)
			}
		})
	}
}

// A second user's accounts must not ride on the first user's request.
func TestScopesAreIndependent(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	seed(t, st, wsKey1, "business", "")
	seed(t, st, wsKey2, "business", "")
	seed(t, st, wsKeyOther, "business", "")
	ac := &accountsCounter{reply: []api.Workspace{{AccountID: "team-1", Name: "Alpha"}, {AccountID: "team-2", Name: "Beta"}}}
	e.refreshNames(ac)
	if len(ac.calls) != 1 {
		t.Fatalf("calls = %v, want 1 (user-b's scope has one record)", ac.calls)
	}
	if got := e.open().SlotForKey(wsKeyOther).WorkspaceName; got != "" {
		t.Fatalf("user-b name = %q, want empty", got)
	}
}

func TestScopeWithNoUsableCredentialsMakesNoRequest(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	mustUpsert(t, st, wsKey1, store.Upsert{Email: "a@x", Plan: "business"})
	mustUpsert(t, st, wsKey2, store.Upsert{Email: "a@x", Plan: "business"})
	ac := &accountsCounter{}
	RefreshWorkspaceNames(context.Background(), e.open(), ac.client(), func(store.Slot) map[string]any { return nil })
	if len(ac.calls) != 0 {
		t.Fatalf("calls = %v, want none", ac.calls)
	}
}

// The first slot with usable credentials asks; slots before it without a
// token or account id are skipped, not fatal.
func TestFirstSlotWithUsableCredentialsAsks(t *testing.T) {
	e := newEnv(t)
	st := e.open()
	mustUpsert(t, st, wsKey1, store.Upsert{Email: "a@x", Plan: "business"})
	seed(t, st, wsKey2, "business", "")
	partial := map[string]any{"tokens": map[string]any{"access_token": "at-only"}}
	ac := &accountsCounter{reply: []api.Workspace{{AccountID: "team-1", Name: "Alpha"}}}
	RefreshWorkspaceNames(context.Background(), e.open(), ac.client(), func(s store.Slot) map[string]any {
		if s.AccountKey == wsKey1 {
			return partial
		}
		return e.payloadFor(s)
	})
	if len(ac.calls) != 1 || ac.calls[0] != (call{"at-team-2", "team-2"}) {
		t.Fatalf("calls = %v, want one from team-2", ac.calls)
	}
}
