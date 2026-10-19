package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/web"
)

type menuAnswer struct {
	index int
	text  string
}

func workflowAnswers(t *testing.T, answers ...menuAnswer) {
	t.Helper()
	old := askMenu
	t.Cleanup(func() { askMenu = old })
	askMenu = func(_ ioStreams, title string, choices []string, _ string) (int, string, bool) {
		if len(answers) == 0 {
			return 0, "", false
		}
		a := answers[0]
		answers = answers[1:]
		if choices != nil && (a.index < 0 || a.index >= len(choices)) {
			t.Fatalf("invalid answer for %s", title)
		}
		return a.index, a.text, true
	}
}

func workflowCommands(t *testing.T) *[][]string {
	t.Helper()
	old := menuExecute
	t.Cleanup(func() { menuExecute = old })
	var commands [][]string
	menuExecute = func(_ ioStreams, args ...string) int {
		commands = append(commands, append([]string(nil), args...))
		return 0
	}
	return &commands
}

func TestTransferMenuPreservesLiteralPathsAndProvider(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "backup '$name.tycswap")
	commands := workflowCommands(t)
	workflowAnswers(t, menuAnswer{0, ""}, menuAnswer{1, ""}, menuAnswer{0, ""}, menuAnswer{0, dest}, menuAnswer{1, ""})
	menuTransfer(ioStreams{err: io.Discard})
	if want := [][]string{{"codex", "export", dest}}; !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands=%v", *commands)
	}
}

func TestTransferMenuRefusesOverwriteAndStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.tycswap")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{path, "-"} {
		t.Run(dest, func(t *testing.T) {
			commands := workflowCommands(t)
			workflowAnswers(t, menuAnswer{0, ""}, menuAnswer{0, ""}, menuAnswer{0, ""}, menuAnswer{0, dest}, menuAnswer{1, ""})
			menuTransfer(ioStreams{err: io.Discard})
			if len(*commands) != 0 {
				t.Fatal("unsafe destination reached export")
			}
		})
	}
	if raw, _ := os.ReadFile(path); string(raw) != "keep" {
		t.Fatal("overwrote existing export")
	}
}

func TestImportMenuKeepsExistingUnlessExplicitlyReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.tycswap")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, replace := range []int{0, 1} {
		t.Run([]string{"keep", "replace"}[replace], func(t *testing.T) {
			commands := workflowCommands(t)
			workflowAnswers(t, menuAnswer{1, ""}, menuAnswer{0, ""}, menuAnswer{0, path}, menuAnswer{replace, ""}, menuAnswer{1, ""})
			menuTransfer(ioStreams{err: io.Discard})
			want := []string{"import"}
			if replace == 1 {
				want = append(want, "--force")
			}
			want = append(want, path)
			if !reflect.DeepEqual(*commands, [][]string{want}) {
				t.Fatal(*commands)
			}
		})
	}
}

func TestGroupMenuHonorsOwnershipBlockers(t *testing.T) {
	oldGroups, oldAccounts := menuGroupViews, menuAccounts
	t.Cleanup(func() { menuGroupViews, menuAccounts = oldGroups, oldAccounts })
	menuGroupViews = func(ioStreams) ([]web.GroupView, error) {
		return []web.GroupView{{ID: "opus", Label: "Opus", AccountBlockers: map[string]string{"1": "in use by default"}}}, nil
	}
	menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
		return []reporting.AccountSnapshot{{Number: "1", Email: "a@example.com"}}, nil
	}
	commands := workflowCommands(t)
	workflowAnswers(t, menuAnswer{0, ""}, menuAnswer{0, ""}, menuAnswer{0, ""})
	var stderr bytes.Buffer
	menuGroups(ioStreams{err: &stderr})
	if len(*commands) != 0 || !strings.Contains(stderr.String(), "in use by default") {
		t.Fatal("blocked group account was offered for switching")
	}
}

func TestGroupSettingsResetInheritsSharedValue(t *testing.T) {
	commands := workflowCommands(t)
	workflowAnswers(t, menuAnswer{0, ""}, menuAnswer{0, ""})
	menuGroupSettings(ioStreams{}, web.GroupView{ID: "opus", Label: "Opus", SettingViews: []web.SettingView{
		{Key: "autoswitch.model", ReadOnly: "follows sessions"},
		{Key: "autoswitch.sevenDayThreshold", Label: "Weekly", Value: 97},
	}})
	if want := [][]string{{"groups", "config", "opus", "--unset", "autoswitch.sevenDayThreshold"}}; !reflect.DeepEqual(*commands, want) {
		t.Fatal(*commands)
	}
}

func TestMenuRenewActiveLoginRequestsActivation(t *testing.T) {
	old := menuAccounts
	t.Cleanup(func() { menuAccounts = old })
	menuAccounts = func(ioStreams) ([]reporting.AccountSnapshot, error) {
		return []reporting.AccountSnapshot{{Number: "1", Email: "a@example.com", IsActive: true}}, nil
	}
	commands := workflowCommands(t)
	workflowAnswers(t, menuAnswer{0, ""}, menuAnswer{5, ""}, menuAnswer{1, ""})
	menuManage(ioStreams{})
	if want := [][]string{{"add", "--login", "--switch", "--", "--email", "a@example.com"}}; !reflect.DeepEqual(*commands, want) {
		t.Fatal(*commands)
	}
}

func TestDashboardReuseAuthenticatesAndDoesNotStartAnotherApp(t *testing.T) {
	appLockHome(t)
	lock, held, err := acquireAppLock()
	if err != nil || !held {
		t.Fatal(err)
	}
	defer lock.Release()
	tokenPath := remoteTokenPath(paths.GetBackupRoot())
	if err := writeRemoteToken(tokenPath, strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	var base string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/launch" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 32) {
			t.Error("invalid authenticated launch request")
			w.WriteHeader(403)
			return
		}
		requests++
		json.NewEncoder(w).Encode(map[string]string{"url": base + "/?token=" + strings.Repeat("b", 32)})
	}))
	defer server.Close()
	base = server.URL
	cleanup, err := publishDashboardConnection(base+"/?token=not-for-disk", tokenPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if raw, _ := os.ReadFile(dashboardConnectionPath(false)); strings.Contains(string(raw), "not-for-disk") {
		t.Fatal("published a launch token")
	}
	oldStart, oldOpen := menuStartApp, openBrowser
	t.Cleanup(func() { menuStartApp, openBrowser = oldStart, oldOpen })
	menuStartApp = func(string, ioStreams) int { t.Fatal("started another app"); return 1 }
	opened := ""
	openBrowser = func(url string) error { opened = url; return nil }
	if code := menuOpenDashboard("tycswap", ioStreams{out: io.Discard, err: io.Discard}); code != 0 || requests != 1 || opened != base+"/?token="+strings.Repeat("b", 32) {
		t.Fatalf("code=%d requests=%d opened=%q", code, requests, opened)
	}
	cleanup()
	if _, err := os.Stat(dashboardConnectionPath(false)); !os.IsNotExist(err) {
		t.Fatal("connection file was not removed")
	}
}

func TestDashboardDiscoveryIgnoresStaleFiles(t *testing.T) {
	appLockHome(t)
	cleanup, err := publishDashboardConnection("http://127.0.0.1:1234", "unused", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if client, running, err := runningDashboard(); client != nil || running || err != nil {
		t.Fatalf("stale connection considered live: %v %v %v", client, running, err)
	}
}

func TestRemoteMenuSectionsAreValidated(t *testing.T) {
	c := newRemoteClient("http://127.0.0.1:1234", "unused")
	var sections []string
	c.accountSetup = func(section string) { sections = append(sections, section) }
	for _, data := range []string{`{}`, `{"section":"groups"}`, `{"section":"transfer"}`, `{"section":"main; echo unsafe"}`, `not json`} {
		c.dispatch("account-setup", []byte(data), nil, nil, nil)
	}
	if !reflect.DeepEqual(sections, []string{"add", "groups", "transfer"}) {
		t.Fatal(sections)
	}
}

func TestGroupSessionUsesChosenProjectAndResume(t *testing.T) {
	old := menuSessionExecute
	t.Cleanup(func() { menuSessionExecute = old })
	dir := t.TempDir()
	var gotDir string
	var gotArgs []string
	menuSessionExecute = func(_ ioStreams, dir string, args ...string) int {
		gotDir, gotArgs = dir, args
		return 0
	}
	workflowAnswers(t, menuAnswer{1, ""}, menuAnswer{0, "session-id"}, menuAnswer{0, dir})
	menuGroupSession(ioStreams{err: io.Discard}, web.GroupView{ID: "opus", ActiveNumber: "2"}, true)
	if gotDir != dir || !reflect.DeepEqual(gotArgs, []string{"run", "--group", "opus", "--model", "sonnet", "--resume", "session-id"}) {
		t.Fatalf("dir=%q args=%v", gotDir, gotArgs)
	}
}
