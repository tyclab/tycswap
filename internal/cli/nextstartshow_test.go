package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
)

func TestShowNextStartReadsOnlyTheRequest(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"fresh", "", "none"},
		{"armed", `{"model":"mythos","id":"original"}`, "mythos"},
		{"cleared", `{"model":"","id":"original"}`, "none"},
		{"damaged", "[", ""},
		{"missing model", `{"id":"original"}`, ""},
		{"missing id", `{"model":"mythos"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := startTestLaunch(t)
			root := paths.GetBackupRoot()
			path := filepath.Join(root, "next-start.json")
			if tc.raw != "" {
				writeFile(t, path, tc.raw)
			}
			want := tc.want
			if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
				want = "none"
			}
			code, out, msg := runSub(t, "start", "--next", "show")
			if want == "" {
				if code != 1 || out != "" || strings.Count(msg, "\n") != 1 || !strings.Contains(msg, path) || !strings.Contains(msg, "tycswap start --next clear") {
					t.Errorf("unreadable request: exit %d, stdout %q, stderr %q", code, out, msg)
				}
			} else if code != 0 || out != want+"\n" || msg != "" {
				t.Errorf("show: exit %d, stdout %q, stderr %q; want %q", code, out, msg, want+"\n")
			}
			if *calls != 0 {
				t.Fatal("show launched a client")
			}
			if tc.raw == "" {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("show created a store: %v", err)
				}
				return
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "next-start.json" || readOrAbsent(t, path) != tc.raw {
				t.Fatalf("show changed the store: entries=%v, err=%v", entries, err)
			}
		})
	}
}

func TestShowNextStartWhileWriterHoldsLock(t *testing.T) {
	startTestLaunch(t)
	root := paths.GetBackupRoot()
	writeFile(t, filepath.Join(root, "next-start.json"), `{"model":"mythos","id":"original"}`)
	err := filelock.New(filepath.Join(root, ".next-start.lock"), 0).With(func() error {
		code, out, msg := runSub(t, "start", "--next", "show")
		want := "none\n"
		if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
			want = "mythos\n"
		}
		if code != 0 || out != want || msg != "" {
			t.Errorf("show under writer lock: exit %d, stdout %q, stderr %q", code, out, msg)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
