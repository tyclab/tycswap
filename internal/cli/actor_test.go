package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/settings"
)

// wantCLIActor is the actor a command run by this test process names.
func wantCLIActor(t *testing.T) string {
	t.Helper()
	actor := "cli(pid=" + strconv.Itoa(os.Getpid())
	if comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(os.Getppid()), "comm")); err == nil {
		actor += ",parent=" + strings.TrimSpace(string(comm))
	}
	return actor + ")"
}

// logLines is tycswap.log's messages, the "<time> - <LEVEL> - " prefix cut.
func logLines(t *testing.T, sw *core.Switcher) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sw.BackupDir(), "tycswap.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if i := strings.Index(line, " - INFO - "); i >= 0 {
			out = append(out, line[i+len(" - INFO - "):])
		}
	}
	return out
}

func countLine(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}

// `tycswap disable` and `enable` name the command and the shell that ran it,
// once, after the change; a refused one names nobody.
func TestCLIDisableLogsActor(t *testing.T) {
	sw := fixtureSwitcher(t)
	useSwitcher(t, sw)
	actor := wantCLIActor(t)
	if code, _, errb := runSub(t, "disable", "2"); code != 0 {
		t.Fatalf("disable 2: exit %d, stderr %q", code, errb)
	}
	if code, _, errb := runSub(t, "enable", "2"); code != 0 {
		t.Fatalf("enable 2: exit %d, stderr %q", code, errb)
	}
	if code, _, _ := runSub(t, "disable", "42"); code == 0 {
		t.Fatal("disable 42 succeeded on a roster without it")
	}
	lines := logLines(t, sw)
	for _, want := range []string{"Disabled account 2 by " + actor, "Enabled account 2 by " + actor} {
		if n := countLine(lines, want); n != 1 {
			t.Errorf("tycswap.log has %d of %q:\n%s", n, want, strings.Join(lines, "\n"))
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "account 42 by") {
			t.Errorf("a refused disable was logged: %q", l)
		}
	}
}

// The switch layer's own line (DESIGN A4) stays byte-exact and the actor
// line follows it; a switch onto the active account names nobody.
func TestSwitchLogsActorBesideA4Line(t *testing.T) {
	sw := fixtureSwitcher(t)
	useSwitcher(t, sw)
	from := sw.CurrentAccountNumber()
	if from == nil || *from != "1" {
		t.Fatalf("fixture live account = %v, want 1", from)
	}
	if code, _, errb := runSub(t, "switch", "2"); code != 0 {
		t.Fatalf("switch 2: exit %d, stderr %q", code, errb)
	}
	if code, _, errb := runSub(t, "switch", "2"); code != 0 {
		t.Fatalf("switch 2 again: exit %d, stderr %q", code, errb)
	}
	lines := logLines(t, sw)
	a4, actor := "Switched from account 1 to 2", "Switched to account 2 by "+wantCLIActor(t)
	at := -1
	for i, l := range lines {
		if l == a4 {
			at = i
		}
	}
	if at < 0 || countLine(lines, a4) != 1 {
		t.Fatalf("the A4 line %q is not in tycswap.log once:\n%s", a4, strings.Join(lines, "\n"))
	}
	var after []string
	for _, l := range lines[at+1:] {
		if strings.HasPrefix(l, "Switched to account") {
			after = append(after, l)
		}
	}
	if len(after) != 1 || after[0] != actor {
		t.Fatalf("actor lines after the switch = %q, want only %q", after, actor)
	}
}

// `config set` and `config unset` name their actor; a refused value and an
// unset of a key that is not set write nothing.
func TestSettingsWriteLogsActor(t *testing.T) {
	sw := fixtureSwitcher(t)
	useSwitcher(t, sw)
	actor := wantCLIActor(t)
	for _, argv := range [][]string{
		{"config", "set", "autoswitch.cooldownSeconds", "120"},
		{"config", "set", "autoswitch.cooldownSeconds", "nope"},
		{"config", "unset", "autoswitch.cooldownSeconds"},
		{"config", "unset", "autoswitch.cooldownSeconds"},
		{"auto", "off"},
	} {
		runSub(t, argv...)
	}
	if got := settings.Load(sw.BackupDir()).CooldownSeconds; got != settings.Default().CooldownSeconds {
		t.Fatalf("cooldown = %v after unset", got)
	}
	lines := logLines(t, sw)
	for want, n := range map[string]int{
		"Set autoswitch.cooldownSeconds=120 by " + actor:  1,
		"Unset autoswitch.cooldownSeconds by " + actor:    1,
		"Set autoswitch.enabled=false by " + actor:        1,
		"Set autoswitch.cooldownSeconds=nope by " + actor: 0,
	} {
		if got := countLine(lines, want); got != n {
			t.Errorf("tycswap.log has %d of %q, want %d:\n%s", got, want, n, strings.Join(lines, "\n"))
		}
	}
}

// A disable through the dashboard's HTTP API names the dashboard, how the
// request authenticated and where it came from: the app logged the 2026-11-15
// toggles of accounts 9 and 10 without either.
func TestDisableOverHTTPLogsActor(t *testing.T) {
	sw := fixtureSwitcher(t)
	useSwitcher(t, sw)
	ctx, cancel := context.WithCancel(context.Background())
	d, code := newDashboard(ctx, 3600, false, ioStreams{in: strings.NewReader(""), out: os.Stdout, err: os.Stderr}, dashboardOptions{remoteToken: "remote-test-token", noUpdateSchedule: true})
	if code != 0 {
		t.Fatalf("newDashboard exit %d", code)
	}
	url, err := d.srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- d.srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("the dashboard did not stop")
		}
		d.auto.stopKeepingChoice()
	})
	base := url[:strings.Index(url, "/?token=")]
	req, _ := http.NewRequest(http.MethodPost, base+"/api/accounts/claude:2/disable", nil)
	req.Header.Set("Authorization", "Bearer remote-test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable over HTTP: status %d", resp.StatusCode)
	}
	var found []string
	for _, l := range logLines(t, sw) {
		if strings.HasPrefix(l, "Disabled account 2 by ") {
			found = append(found, l)
		}
	}
	if len(found) != 1 || !strings.HasPrefix(found[0], "Disabled account 2 by dashboard(bearer,127.0.0.1:") {
		t.Fatalf("actor lines = %q, want one naming dashboard(bearer,127.0.0.1:<port>)", found)
	}
}
