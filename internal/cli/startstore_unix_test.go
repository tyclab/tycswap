//go:build !windows

// Tests of tycswap start that need named pipes (DESIGN A103).
package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe where start looks for the resumed session's transcript or a
// settings file names no model and does not hang start: it reads regular
// files only.
func TestSessionFamilySkipsANamedPipe(t *testing.T) {
	for name, path := range map[string]func(familyFiles) string{
		"transcript": func(f familyFiles) string {
			return filepath.Join(f.home, ".claude", "projects", "-work", resumeID+".jsonl")
		},
		"local settings": func(f familyFiles) string { return filepath.Join(f.cwd, ".claude", "settings.local.json") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFamilyFiles(t)
			fifo := path(f)
			if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			// A reader blocked on the pipe is let go when the test ends.
			t.Cleanup(func() {
				if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = w.Close()
				}
			})
			f.userSettings(t, `{"model":"opus"}`)
			done := make(chan [2]string, 1)
			go func() {
				family, from := sessionFamily([]string{"--resume", resumeID}, nil, f.cwd, f.home, f.root)
				done <- [2]string{family, from}
			}()
			select {
			case got := <-done:
				if want := filepath.Join(f.home, ".claude", "settings.json"); got != [2]string{"opus", want} {
					t.Fatalf("sessionFamily = %q, want opus from %s", got, want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("sessionFamily blocked on a named pipe")
			}
		})
	}
}
