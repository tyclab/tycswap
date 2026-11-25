package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/paths"
)

func TestRepairPIDOnlyRegistryRequiresCurrentLaunch(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := startWithoutHook(t, rec, "opus")
	t.Setenv(secureStorageEnv, "")
	pid := os.Getpid()
	record := filepath.Join(paths.GetClaudeConfigHome(), "sessions", fmt.Sprintf("%d.json", pid))
	writeFile(t, record, fmt.Sprintf(`{"pid":%d,"sessionId":%q}`, pid, placementSession))
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset time.Duration
		exit   int
	}{{-time.Hour, 1}, {time.Second, 0}} {
		modified := info.ModTime().Add(tc.offset)
		if err := os.Chtimes(record, modified, modified); err != nil {
			t.Fatal(err)
		}
		code, _, msg := runCLI(t, []string{"use", "fable", "--session", placementSession}, false, false)
		if code != tc.exit {
			t.Fatalf("registry age relative to session link %v: exit %d, want %d: %s", tc.offset, code, tc.exit, msg)
		}
		if code == 1 {
			checkLinkTarget(t, link, sw.DefaultProfileDir())
		}
	}
	checkLinkTarget(t, link, fableStoreDir())
}
