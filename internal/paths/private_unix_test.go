//go:build !windows

package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPrivateRoot(t *testing.T) {
	base := t.TempDir()
	if w, err := CheckPrivateRoot(filepath.Join(base, "absent")); err != nil || w != "" {
		t.Errorf("absent root: %q, %v", w, err)
	}
	private := filepath.Join(base, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if w, err := CheckPrivateRoot(private); err != nil || w != "" {
		t.Errorf("0700 root: %q, %v", w, err)
	}
	// Writable by group or other: made 0700, silently.
	for _, mode := range []os.FileMode{0o770, 0o757, 0o777} {
		if err := os.Chmod(private, mode); err != nil {
			t.Fatal(err)
		}
		w, err := CheckPrivateRoot(private)
		if err != nil || w != "" {
			t.Errorf("mode %04o: %q, %v, want the root made private without a word", mode, w, err)
		}
		if fi, err := os.Stat(private); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("mode %04o: left as %v, want 0700", mode, fi.Mode().Perm())
		}
	}
	// Readable by others is not a write risk and is left alone.
	if err := os.Chmod(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if w, err := CheckPrivateRoot(private); err != nil || w != "" {
		t.Errorf("0755 root: %q, %v", w, err)
	}
	if fi, _ := os.Stat(private); fi.Mode().Perm() != 0o755 {
		t.Errorf("0755 root changed to %v", fi.Mode().Perm())
	}
	// A file is not a store.
	f := filepath.Join(base, "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckPrivateRoot(f); err == nil {
		t.Error("a file passed as the store root")
	}
	// Owned by someone else: only testable as a non-root user against a
	// root-owned directory.
	if os.Geteuid() != 0 {
		if fi, err := os.Stat("/"); err == nil && fi.Mode().Perm()&0o022 == 0 {
			if _, err := CheckPrivateRoot("/"); err == nil || !strings.Contains(err.Error(), "owned by uid 0") {
				t.Errorf("root-owned dir: err = %v", err)
			}
		}
	}
}

// TestCheckPrivateRootWarnsWhenChmodChangesNothing: on a filesystem that
// ignores chmod the mode stays 0777; the root is used and one warning names
// the quoted path. A chmod that fails outright is the same case.
func TestCheckPrivateRootWarnsWhenChmodChangesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "it's here")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	// Whatever the real mode, the stand-in reports 0777 before and after.
	t.Cleanup(func() { PrivateRootStat, PrivateRootChmod = os.Stat, os.Chmod })
	PrivateRootStat = func(name string) (os.FileInfo, error) {
		fi, err := os.Stat(name)
		if err != nil {
			return nil, err
		}
		return modeInfo{fi, fi.Mode()&^0o777 | 0o777}, nil
	}
	chmods := 0
	for _, chmod := range []func(string, os.FileMode) error{
		func(string, os.FileMode) error { chmods++; return nil },
		func(string, os.FileMode) error { chmods++; return errors.New("operation not permitted") },
	} {
		PrivateRootChmod = chmod
		w, err := CheckPrivateRoot(root)
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		want := "chmod 700 '" + strings.ReplaceAll(root, "'", `'\''`) + "'"
		if !strings.Contains(w, "did not change") || !strings.Contains(w, want) || strings.Contains(w, "\n") {
			t.Errorf("warning = %q, want one line with %q", w, want)
		}
	}
	if chmods != 2 {
		t.Errorf("chmod attempted %d times, want once per check", chmods)
	}
}

// modeInfo is a FileInfo with its mode replaced.
type modeInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (m modeInfo) Mode() os.FileMode { return m.mode }
