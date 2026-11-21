package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

func startWithoutHook(t *testing.T, rec *startRecord, model string) string {
	t.Helper()
	startResolve = func(string) (string, error) { return "/fixture/claude", nil }
	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", model}, false, false); code != 23 {
		t.Fatalf("launch = %d: %s", code, msg)
	}
	for _, name := range []string{secureStorageEnv, startFamilyEnv, recoveredFableEnv} {
		t.Setenv(name, envValue(rec.env, name))
	}
	return envValue(rec.env, secureStorageEnv)
}

func sessionRegistryEntry(t *testing.T, id, domain string) {
	t.Helper()
	pid := os.Getpid()
	start, ok := procdetect.ProcStartTime(pid)
	if !ok {
		t.Fatal("missing test process start time")
	}
	writeFile(t, filepath.Join(paths.GetClaudeConfigHome(), "sessions", fmt.Sprintf("%d.json", pid)),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"procStart":%q,"pidDomain":%q}`, pid, id, start, domain))
}

func TestRepairSessionAtFirstObservation(t *testing.T) {
	for _, observe := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto=%t", observe), func(t *testing.T) {
			sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
			link := startWithoutHook(t, rec, "opus")
			line := sessionStatus(t, link, placementSession, "claude-fable-5-2", observe)
			linked, ok, err := reporting.LookupSessionLink(sw.BackupDir(), placementSession)
			if err != nil || !ok || linked.Token != filepath.Base(link) || linked.Family != "fable" || line != "" {
				t.Fatalf("first observation after missed hook: record=%+v, ok=%t, err=%v, line=%q", linked, ok, err, line)
			}
			checkLinkTarget(t, link, fableStoreDir())
		})
	}
}

func TestRepairOlderWriterPreservesRecoveredFable(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := linkedTestStart(t, rec, placementSession, "fable")
	binding := "fable/" + store.AccountIdentityDigest(fableEmail("2"), "")
	p := filepath.Join(sw.BackupDir(), "default_models.json")
	writeFile(t, p, fmt.Sprintf(`{"sessions":{%q:{"family":"opus","seenAt":%d,"store":"fable","binding":%q}}}`, placementSession, time.Now().Unix(), binding))
	t.Setenv(recoveredFableEnv, "")
	line := sessionStatus(t, link, placementSession, "claude-fable-5-2", false)
	saved, ok, err := reporting.LookupSessionLink(sw.BackupDir(), placementSession)
	actual, bindErr := reporting.SessionBinding(sw.BackupDir(), placementSession)
	if err != nil || !ok || saved.Family != "fable" || saved.Token != filepath.Base(link) || actual != binding || bindErr != nil || line != "" {
		t.Fatalf("older writer: record=%+v, ok=%t, err=%v; binding=%q, %v; line=%q", saved, ok, err, actual, bindErr, line)
	}
}

func TestRepairSessionUseFromProcessRegistry(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprintf("outside=%t", outside), func(t *testing.T) {
			sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
			link := startWithoutHook(t, rec, "opus")
			args := []string{"use", "fable"}
			if outside {
				t.Setenv(secureStorageEnv, "")
				t.Setenv(startFamilyEnv, "")
				args = append(args, "--session", placementSession)
			}
			sessionRegistryEntry(t, placementSession, "")
			if code, _, msg := runCLI(t, args, false, false); code != 0 {
				t.Fatalf("use with a registered process and missing hook: %d %s", code, msg)
			}
			checkLinkTarget(t, link, fableStoreDir())
			saved, ok, err := reporting.LookupSessionLink(sw.BackupDir(), placementSession)
			if err != nil || !ok || saved.Token != filepath.Base(link) || saved.Wanted != "fable" {
				t.Fatalf("saved placement = %+v, %t, %v", saved, ok, err)
			}
		})
	}
}

func TestRepairSessionRefusesAnotherOwnersLink(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	const other = "6dfa671a-4c88-49fe-832a-5afcd46f9d2d"
	link := linkedTestStart(t, rec, other, "opus")
	line := sessionStatus(t, link, placementSession, "claude-fable-5-2", false)
	if line == "" {
		t.Fatal("observer claimed another session's link")
	}
	checkLinkTarget(t, link, sw.DefaultProfileDir())
	if _, found, err := reporting.LookupSessionLink(sw.BackupDir(), placementSession); err != nil || found {
		t.Fatalf("borrowed link became a binding: found=%t, %v", found, err)
	}
	own := startWithoutHook(t, rec, "opus")
	if line := sessionStatus(t, own, placementSession, "claude-fable-5-2", false); line != "" {
		t.Fatalf("observer still refuses its own unbound link: %s", line)
	}
	checkLinkTarget(t, own, fableStoreDir())
	checkLinkTarget(t, link, sw.DefaultProfileDir())
}

func TestRepairSessionOutsideRequiresUniqueLocalOwner(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := startWithoutHook(t, rec, "opus")
	t.Setenv(secureStorageEnv, "")
	use := func(want int) {
		t.Helper()
		if code, _, msg := runCLI(t, []string{"use", "fable", "--session", placementSession}, false, false); code != want {
			t.Fatalf("outside use = %d, want %d: %s", code, want, msg)
		}
	}
	sessionRegistryEntry(t, placementSession, "linux:another-machine:pid:[1]")
	use(1)
	checkLinkTarget(t, link, sw.DefaultProfileDir())
	sessionRegistryEntry(t, placementSession, "")
	extra, err := reporting.CreateSessionLink(sw.BackupDir(), sw.DefaultProfileDir())
	if err != nil {
		t.Fatal(err)
	}
	use(1)
	checkLinkTarget(t, extra, sw.DefaultProfileDir())
	checkLinkTarget(t, link, sw.DefaultProfileDir())
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	use(0)
	checkLinkTarget(t, link, fableStoreDir())
}

func TestRepairDoesNotEnableSharedMythosOnModelChange(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, off: true})
	link := startWithoutHook(t, rec, "opus")
	line := sessionStatus(t, link, placementSession, "claude-mythos-5-2", false)
	saved, ok, err := reporting.LookupSessionLink(sw.BackupDir(), placementSession)
	if err != nil || !ok || saved.Family != "mythos" || saved.SharedMythos || !strings.Contains(strings.ToLower(line), "mythos") {
		t.Fatalf("switched model after missed hook: record=%+v, ok=%t, err=%v, line=%q", saved, ok, err, line)
	}
	checkLinkTarget(t, link, sw.DefaultProfileDir())
}
