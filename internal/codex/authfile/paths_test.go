// paths_test.go — Codex path resolution: ~/.codex on the read side, tycswap's
// own store on the write side. Ports claude-swap PR #252
// tests/test_codex_paths.py.

package authfile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestHome_DefaultsToDotCodex(t *testing.T) {
	home := isolate(t)
	if got, want := Home(), filepath.Join(home, ".codex"); got != want {
		t.Fatalf("Home() = %q, want %q", got, want)
	}
}

// The codex CLI reads CODEX_HOME; tycswap must resolve the same file it does.
func TestHome_HonoursTheEnvVar(t *testing.T) {
	home := isolate(t)
	testutil.Setenv(t, "CODEX_HOME", filepath.Join(home, "elsewhere"))
	if got, want := Home(), filepath.Join(home, "elsewhere"); got != want {
		t.Fatalf("Home() = %q, want %q", got, want)
	}
}

// An empty CODEX_HOME counts as unset, as Python's `if env:` does.
func TestHome_EmptyEnvVarFallsBack(t *testing.T) {
	home := isolate(t)
	testutil.Setenv(t, "CODEX_HOME", "")
	if got, want := Home(), filepath.Join(home, ".codex"); got != want {
		t.Fatalf("Home() = %q, want %q", got, want)
	}
}

func TestCodexHomePaths(t *testing.T) {
	home := isolate(t)
	codex := filepath.Join(home, ".codex")
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"live auth sits directly in codex home", LiveAuthPath(), filepath.Join(codex, "auth.json")},
		{"legacy registry points at codex-auth data", AuthRegistryPath(), filepath.Join(codex, "accounts", "registry.json")},
		{"codex-auth accounts dir", AuthAccountsDir(), filepath.Join(codex, "accounts")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestStoreRoot_IsASubtreeOfTheBackupRoot(t *testing.T) {
	home := isolate(t)
	root := StoreRoot()
	if want := filepath.Join(paths.GetBackupRoot(), "codex"); root != want {
		t.Fatalf("StoreRoot() = %q, want %q", root, want)
	}
	if filepath.Dir(root) != paths.GetBackupRoot() {
		t.Fatalf("parent of %q is not the backup root %q", root, paths.GetBackupRoot())
	}
	if rel, err := filepath.Rel(home, root); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("store root %q escaped the temp home %q", root, home)
	}
}

func TestStorePaths_HangOffTheStoreRoot(t *testing.T) {
	isolate(t)
	root := StoreRoot()
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"sequence", SequencePath(), filepath.Join(root, "sequence.json")},
		{"credentials", CredentialsDir(), filepath.Join(root, "credentials")},
		{"cache", CacheDir(), filepath.Join(root, "cache")},
		{"lock", LockPath(), filepath.Join(root, ".lock")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}
