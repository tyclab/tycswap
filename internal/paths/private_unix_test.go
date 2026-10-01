//go:build !windows

package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPrivateRoot(t *testing.T) {
	base := t.TempDir()
	if err := CheckPrivateRoot(filepath.Join(base, "absent")); err != nil {
		t.Errorf("absent root: %v", err)
	}
	private := filepath.Join(base, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateRoot(private); err != nil {
		t.Errorf("0700 root: %v", err)
	}
	for _, mode := range []os.FileMode{0o770, 0o757, 0o777} {
		if err := os.Chmod(private, mode); err != nil {
			t.Fatal(err)
		}
		err := CheckPrivateRoot(private)
		if err == nil || !strings.Contains(err.Error(), private) || !strings.Contains(err.Error(), "group or other") {
			t.Errorf("mode %04o: err = %v, want a refusal naming the path", mode, err)
		}
	}
	// Readable by others is not a write risk.
	if err := os.Chmod(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateRoot(private); err != nil {
		t.Errorf("0755 root: %v", err)
	}
	// A file is not a store.
	f := filepath.Join(base, "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateRoot(f); err == nil {
		t.Error("a file passed as the store root")
	}
	// Owned by someone else: only testable as a non-root user against a
	// root-owned directory.
	if os.Geteuid() != 0 {
		if fi, err := os.Stat("/"); err == nil && fi.Mode().Perm()&0o022 == 0 {
			if err := CheckPrivateRoot("/"); err == nil || !strings.Contains(err.Error(), "owned by uid 0") {
				t.Errorf("root-owned dir: err = %v", err)
			}
		}
	}
}
