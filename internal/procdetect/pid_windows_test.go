package procdetect

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestIgnoreStatError: the errors os.Stat returns on Windows read as pathlib's
// _ignore_error reads them there. A path that can name no directory (missing,
// below a missing directory or a file, an invalid name, a drive not ready, a
// link that cannot be resolved) is absent; an access denial, or an error with
// no code, is surfaced so the probe fails closed. The errors a test cannot
// cause without changing the machine (a drive not ready, a link that cannot
// be resolved, a denial) are built; the others come from os.Stat.
func TestIgnoreStatError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stat := func(p string) error {
		t.Helper()
		_, err := os.Stat(p)
		if err == nil {
			t.Fatalf("stat %s succeeded", p)
		}
		return err
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"missing", stat(filepath.Join(dir, "missing")), true},
		{"below a missing directory", stat(filepath.Join(dir, "missing", "sessions")), true},
		{"below a file", stat(filepath.Join(file, "sessions")), true},
		{"invalid name", stat(filepath.Join(dir, "bad<name>")), true},
		{"drive not ready", &fs.PathError{Op: "GetFileAttributesEx", Path: `A:\sessions`, Err: windows.ERROR_NOT_READY}, true},
		{"link not resolved", &fs.PathError{Op: "CreateFile", Path: dir, Err: windows.ERROR_CANT_RESOLVE_FILENAME}, true},
		{"access denied", &fs.PathError{Op: "CreateFile", Path: dir, Err: windows.ERROR_ACCESS_DENIED}, false},
		{"no code", errors.New("the lookup failed"), false},
	} {
		if got := ignoreStatError(tc.err); got != tc.want {
			t.Errorf("%s: ignoreStatError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
