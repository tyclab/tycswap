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

func TestRepairRespectsTokenInDamagedSibling(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := startWithoutHook(t, rec, "opus")
	const sibling = "af3347ac-814a-44e0-960d-4f21d07244c1"
	file := filepath.Join(sw.BackupDir(), "default_models.json")
	damaged := fmt.Sprintf(`{"sessions":{%q:{"family":"opus","link":{"token":%q}}}}`, sibling, filepath.Base(link))
	writeFile(t, file, damaged)
	if msg := sessionStatus(t, link, placementSession, "fable", false); msg == "" {
		t.Fatal("repair claimed a token named by another session")
	}
	if got := readOrAbsent(t, file); got != damaged {
		t.Fatalf("mutated damaged record: %s", got)
	}
	checkLinkTarget(t, link, sw.DefaultProfileDir())
	own := startWithoutHook(t, rec, "opus")
	if msg := sessionStatus(t, own, placementSession, "fable", false); msg != "" {
		t.Fatalf("unrelated damaged row prevented repair of the session's own link: %s", msg)
	}
	checkLinkTarget(t, own, fableStoreDir())
	checkLinkTarget(t, link, sw.DefaultProfileDir())
}
