package groups

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSafePathDarwinSystemAliases(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin filesystem aliases")
	}
	for alias, target := range map[string]string{"/var": "/private/var", "/tmp": "/private/tmp"} {
		t.Run(alias, func(t *testing.T) {
			info, err := os.Lstat(alias)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Skip("this system uses a direct directory")
			}
			resolved, err := filepath.EvalSymlinks(alias)
			if err != nil || resolved != target {
				t.Skip("this system has a nonstandard alias")
			}
			path := filepath.Join(alias, "tycswap-missing-path-test", "groups", "fable", "profile", "settings.json")
			if err := SafePath(path); err != nil {
				t.Fatalf("standard OS alias blocked a managed path: %v", err)
			}
		})
	}
}

func TestSafePathStillRejectsCustomAndManagedSymlinks(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "global-settings.json")
	original := []byte(`{"preserve":"global"}`)
	if err := os.WriteFile(global, original, 0o600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "groups", "fable", "profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedFile := filepath.Join(profile, "settings.json")
	if err := os.Symlink(global, linkedFile); err != nil {
		t.Fatal(err)
	}
	linkedProfile := filepath.Join(root, "groups", "opus", "profile")
	if err := os.MkdirAll(filepath.Dir(linkedProfile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(profile, linkedProfile); err != nil {
		t.Fatal(err)
	}
	customAlias := filepath.Join(root, "var")
	if err := os.Symlink(root, customAlias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{linkedFile, filepath.Join(linkedProfile, "other.json"), filepath.Join(customAlias, "groups", "fable", "profile", "other.json")} {
		if err := SafePath(path); err == nil {
			t.Fatalf("custom or managed symlink was accepted: %s", path)
		}
		if err := WriteJSON(path, map[string]string{"replace": "blocked"}); err == nil {
			t.Fatalf("symlink write was accepted: %s", path)
		}
	}
	if after, err := os.ReadFile(global); err != nil || string(after) != string(original) {
		t.Fatal("managed symlink changed the global settings")
	}
}
