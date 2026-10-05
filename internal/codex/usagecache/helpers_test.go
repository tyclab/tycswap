// helpers_test.go — shared fixtures for the usagecache tests: a temp-rooted
// Codex store, auth.json-shaped payloads, and a counting api.FakeClient.
// Mirrors the codex_home / counter / calls fixtures of claude-swap PR #252
// tests/test_codex_usage_cache.py and tests/test_codex_workspaces.py.

package usagecache

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/store"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/testutil"
)

const t0 = "2026-01-02T03:04:05Z"

type env struct {
	root string
	kc   *keychain.Fake
	clk  *clock.Fake
}

// newEnv roots everything in t.TempDir() and points HOME/CODEX_HOME/
// XDG_DATA_HOME there too, so nothing can reach the real ~/.codex or backup
// root even through a default path.
func newEnv(t *testing.T) *env {
	t.Helper()
	home := testutil.IsolateHome(t)
	testutil.Setenv(t, "CODEX_HOME", filepath.Join(home, ".codex"))
	testutil.Setenv(t, "XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return &env{
		root: filepath.Join(home, "store", "codex"),
		kc:   keychain.NewFake(),
		clk:  testutil.FixedClock(t, t0),
	}
}

// open is `CodexStore()`: a fresh Store over the same root.
func (e *env) open() *store.Store {
	return store.New(store.Options{Root: e.root, Keychain: e.kc, Clock: e.clk, Platform: platform.Linux})
}

func (e *env) payloadFor(s store.Slot) map[string]any {
	return e.open().ReadSnapshot(s.AccountKey)
}

func mustUpsert(t *testing.T, st *store.Store, key string, u store.Upsert) store.Slot {
	t.Helper()
	sl, err := st.UpsertSlot(key, u)
	if err != nil {
		t.Fatalf("UpsertSlot(%s): %v", key, err)
	}
	return sl
}

func mustSnapshot(t *testing.T, st *store.Store, key, accountID string) {
	t.Helper()
	if err := st.WriteSnapshot(key, authJSON(accountID)); err != nil {
		t.Fatalf("WriteSnapshot(%s): %v", key, err)
	}
}

// authJSON is make_auth_json reduced to what these packages read: the tokens
// block's access token and account id.
func authJSON(accountID string) map[string]any {
	return map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      "id-" + accountID,
			"access_token":  "at-" + accountID,
			"refresh_token": "rt-" + accountID,
			"account_id":    accountID,
		},
		"last_refresh": "2026-08-16T00:00:00Z",
	}
}

type call struct{ token, accountID string }

// usageCounter counts FetchUsage calls and returns whatever result holds.
type usageCounter struct {
	mu     sync.Mutex
	calls  []call
	result api.UsageFetch
}

func (u *usageCounter) set(r api.UsageFetch) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.result = r
}

func (u *usageCounter) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *usageCounter) client() *api.FakeClient {
	return &api.FakeClient{UsageFn: func(_ context.Context, token, acct string) api.UsageFetch {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.calls = append(u.calls, call{token, acct})
		return u.result
	}}
}

// accountsCounter records FetchAccounts calls and returns reply/err.
type accountsCounter struct {
	calls []call
	reply []api.Workspace
	err   error
}

func (a *accountsCounter) client() *api.FakeClient {
	return &api.FakeClient{AccountsFn: func(_ context.Context, token, acct string) ([]api.Workspace, error) {
		a.calls = append(a.calls, call{token, acct})
		return a.reply, a.err
	}}
}
