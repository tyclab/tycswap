package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

var retireEmails = map[string]string{"1": "a@example.test", "2": "b@example.test", "3": "c@example.test"}

func retireCreds(access, refresh string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":4102444800000}}`
}

// retireStore: accounts 1 to 3, 3 disabled, each with its stored login; 1 is
// the live login. Former `run`/`env` profiles are added per test.
func retireStore(t *testing.T, oc *oauth.FakeClient) *core.Switcher {
	t.Helper()
	cleanHome(t)
	clk := testutil.FixedClock(t, "2026-11-16T00:00:00Z")
	sw, err := core.New(store.Options{Clock: clk, Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}, OAuth: oc})
	if err != nil {
		t.Fatal(err)
	}
	sw.Creds = credstore.New(credstore.Config{Platform: platform.Linux, CredentialsDir: sw.CredentialsDir}, sw.Keychain(), clk, sw.Log)
	if err := sw.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	accounts := map[string]json.RawMessage{}
	for num, email := range retireEmails {
		accounts[num], _ = json.Marshal(map[string]any{"email": email, "organizationUuid": "", "added": "t", "disabled": num == "3"})
		if err := sw.WriteAccountCredentials(num, email, retireCreds("at-"+num, "rt-"+num)); err != nil {
			t.Fatal(err)
		}
		if err := sw.WriteAccountConfig(num, email, retireConfig(email)); err != nil {
			t.Fatal(err)
		}
	}
	active := 1
	if err := sw.WriteSequence(&store.SequenceData{ActiveAccountNumber: &active, LastUpdated: "2026-11-16T00:00:00Z", Sequence: []int{1, 2, 3}, Accounts: accounts}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sw.Home, ".claude.json"), retireConfig(retireEmails["1"]), 0o600)
	writeTestFile(t, filepath.Join(sw.Home, ".claude", ".credentials.json"), retireCreds("at-1", "rt-1"), 0o600)
	useSwitcher(t, sw)
	return sw
}

func retireConfig(email string) string {
	return `{"oauthAccount":{"emailAddress":"` + email + `","organizationUuid":""}}`
}

// legacyProfile writes account num's former profile as `tycswap run` left
// it: its identity, its login ("" for none), one conversation, and on request
// its claim and the stale mark.
func legacyProfile(t *testing.T, sw *core.Switcher, num, login string, claim, stale bool) string {
	t.Helper()
	email := retireEmails[num]
	dir := sw.SessionDir(num, email)
	writeTestFile(t, filepath.Join(dir, ".claude.json"), retireConfig(email), 0o600)
	writeTestFile(t, filepath.Join(dir, "projects", "-work", "s"+num+".jsonl"), "conversation of "+num+"\n", 0o600)
	if login != "" {
		writeTestFile(t, filepath.Join(dir, ".credentials.json"), login, 0o600)
	}
	if stale {
		writeTestFile(t, filepath.Join(dir, ".tycswap-stale-credentials"), "", 0o600)
	}
	if claim {
		registry, err := groups.LoadRegistry(sw.BackupDir())
		if err != nil {
			t.Fatal(err)
		}
		registry.Claims[num] = groups.Owner{Scope: "legacy:" + num, ProfileDir: dir, Account: groups.Account{Number: num, Email: email}}
		if err := registry.Save(sw.BackupDir()); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func claimed(t *testing.T, sw *core.Switcher, num string) bool {
	t.Helper()
	registry, err := groups.LoadRegistry(sw.BackupDir())
	if err != nil {
		t.Fatal(err)
	}
	_, ok := registry.Claims[num]
	return ok
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// credentialFiles is every stored and live login on disk, for "never written".
func credentialFiles(t *testing.T, sw *core.Switcher) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(sw.CredentialsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(sw.CredentialsDir, e.Name()))
		out[e.Name()] = string(raw)
	}
	for _, path := range []string{filepath.Join(sw.Home, ".claude.json"), filepath.Join(sw.Home, ".claude", ".credentials.json")} {
		raw, _ := os.ReadFile(path)
		out[path] = string(raw)
	}
	return out
}

func sameFiles(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("credential files changed: %d before, %d after", len(before), len(after))
	}
	for name, body := range before {
		if after[name] != body {
			t.Fatalf("%s was written", name)
		}
	}
}

// The timer's tick, here with auto-switch off: only the retirement runs.
func scheduledTick(t *testing.T) {
	t.Helper()
	if code, out, errb := runCodex(t, "", "auto", "--once", "--json"); code != 2 {
		t.Fatalf("auto --once = %d, stdout %q, stderr %q", code, out, errb)
	}
}

func TestStaleClaimOnLiveAccountIsReleased(t *testing.T) {
	sw := retireStore(t, &oauth.FakeClient{})
	dir := legacyProfile(t, sw, "1", retireCreds("at-old", "rt-old"), true, true)
	before := credentialFiles(t, sw)

	if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	runCodex(t, nearBarLine, "auto", "--observe", "--json")
	runCodex(t, "", "auto", "--once", "--dry-run", "--json")
	if !claimed(t, sw, "1") || !exists(filepath.Join(dir, ".credentials.json")) {
		t.Fatal("a status line's tick or a dry run released the profile")
	}

	if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	scheduledTick(t)
	if claimed(t, sw, "1") || exists(filepath.Join(dir, ".credentials.json")) || exists(filepath.Join(dir, ".tycswap-stale-credentials")) {
		t.Fatal("the scheduled tick kept the live account's stale profile login or claim")
	}
	sameFiles(t, before, credentialFiles(t, sw))
	if got, _ := os.ReadFile(filepath.Join(sw.Home, ".claude", "projects", "-work", "s1.jsonl")); string(got) != "conversation of 1\n" {
		t.Fatalf("the profile's conversation did not reach the default profile: %q", got)
	}
}

func TestLiveAccountReleaseNeverWritesBackup(t *testing.T) {
	for name, login := range map[string]string{"duplicate": retireCreds("at-1", "rt-1"), "same lineage": retireCreds("at-1-older", "rt-1"), "empty": ""} {
		t.Run(name, func(t *testing.T) {
			sw := retireStore(t, &oauth.FakeClient{})
			dir := legacyProfile(t, sw, "1", login, true, false)
			before := credentialFiles(t, sw)
			scheduledTick(t)
			if claimed(t, sw, "1") || exists(filepath.Join(dir, ".credentials.json")) {
				t.Fatal("the live account's profile kept its login or claim")
			}
			scheduledTick(t)
			sameFiles(t, before, credentialFiles(t, sw))
			if logged, _ := os.ReadFile(filepath.Join(sw.BackupDir(), "tycswap.log")); strings.Contains(string(logged), "retirement held") {
				t.Fatalf("a retired profile was held on the next tick: %s", logged)
			}
		})
	}
}

func TestDifferentLineageKeepsClaim(t *testing.T) {
	sw := retireStore(t, &oauth.FakeClient{})
	login := retireCreds("at-x", "rt-x")
	dir := legacyProfile(t, sw, "1", login, true, false)
	before := credentialFiles(t, sw)
	scheduledTick(t)
	if got, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); !claimed(t, sw, "1") || string(got) != login {
		t.Fatal("a profile login of another lineage lost its claim or login")
	}
	sameFiles(t, before, credentialFiles(t, sw))
	logged, _ := os.ReadFile(filepath.Join(sw.BackupDir(), "tycswap.log"))
	if !strings.Contains(string(logged), "Account-1 profile retirement held") {
		t.Fatalf("the held claim was not logged: %s", logged)
	}
	scheduledTick(t)
	if n := heldWarnings(t, sw, "1"); n != 1 {
		t.Fatalf("held warnings = %d after a second round, want 1", n)
	}
}

func TestProfileHistoryCopiedOnce(t *testing.T) {
	sw := retireStore(t, &oauth.FakeClient{})
	dir := legacyProfile(t, sw, "2", "", false, false)
	writeTestFile(t, filepath.Join(dir, "projects", "-work", "both.jsonl"), "profile copy\n", 0o600)
	writeTestFile(t, filepath.Join(dir, "projects", "-work", "sub", "agent.jsonl"), "subagent\n", 0o600)
	work := filepath.Join(sw.Home, ".claude", "projects", "-work")
	writeTestFile(t, filepath.Join(work, "both.jsonl"), "default's own\n", 0o600)

	scheduledTick(t)
	for rel, want := range map[string]string{"s2.jsonl": "conversation of 2\n", "both.jsonl": "default's own\n", "sub/agent.jsonl": "subagent\n"} {
		if got, _ := os.ReadFile(filepath.Join(work, rel)); string(got) != want {
			t.Fatalf("%s = %q, want %q", rel, got, want)
		}
	}
	if err := os.Remove(filepath.Join(work, "s2.jsonl")); err != nil {
		t.Fatal(err)
	}
	scheduledTick(t)
	entries, _ := os.ReadDir(work)
	if len(entries) != 2 || exists(filepath.Join(work, "s2.jsonl")) {
		t.Fatalf("a second tick copied again: %v", entries)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "projects", "-work", "s2.jsonl")); string(got) != "conversation of 2\n" {
		t.Fatal("the profile's own conversation changed")
	}
}

// Retirement may ask about a disabled account's leftover login; it never
// enables that account or switches to it.
func TestDisabledAccountProfileRetires(t *testing.T) {
	sw := retireStore(t, &oauth.FakeClient{})
	login := retireCreds("at-3", "rt-3")
	dir := legacyProfile(t, sw, "3", login, true, false)
	scheduledTick(t)
	if claimed(t, sw, "3") || exists(filepath.Join(dir, ".credentials.json")) {
		t.Fatal("the disabled account's idle profile kept its login or claim")
	}
	if got, _ := sw.ReadAccountCredentials("3", retireEmails["3"]); got != login {
		t.Fatalf("the disabled account's login was not kept in its slot: %q", got)
	}
	if !sw.IsAccountDisabled("3") || *sw.CurrentAccountNumber() != "1" {
		t.Fatal("retiring the disabled account's profile enabled it or switched to it")
	}
}

// A namesake the default profile already has is not replaced; one of
// another size is logged with both paths and sizes.
func TestSkippedNamesakeLogged(t *testing.T) {
	sw := retireStore(t, &oauth.FakeClient{})
	dir := legacyProfile(t, sw, "2", "", false, false)
	work := filepath.Join(sw.Home, ".claude", "projects", "-work")
	writeTestFile(t, filepath.Join(dir, "projects", "-work", "differs.jsonl"), "profile copy\n", 0o600)
	writeTestFile(t, filepath.Join(work, "differs.jsonl"), "default's own\n", 0o600)
	writeTestFile(t, filepath.Join(dir, "projects", "-work", "same.jsonl"), "same size A\n", 0o600)
	writeTestFile(t, filepath.Join(work, "same.jsonl"), "same size B\n", 0o600)
	scheduledTick(t)
	if got, _ := os.ReadFile(filepath.Join(work, "differs.jsonl")); string(got) != "default's own\n" {
		t.Fatalf("a namesake was replaced: %q", got)
	}
	logged, _ := os.ReadFile(filepath.Join(sw.BackupDir(), "tycswap.log"))
	line := ""
	for _, l := range strings.Split(string(logged), "\n") {
		if strings.Contains(l, "differs.jsonl") {
			line = l
		}
		if strings.Contains(l, "same.jsonl") {
			t.Fatalf("a same-size namesake was logged: %s", l)
		}
	}
	for _, want := range []string{filepath.Join(dir, "projects", "-work", "differs.jsonl"), filepath.Join(work, "differs.jsonl"), "13 bytes", "14 bytes"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the skipped namesake's line %q lacks %q", line, want)
		}
	}
}

func TestInterruptedReleaseReruns(t *testing.T) {
	t.Run("live account", func(t *testing.T) {
		sw := retireStore(t, &oauth.FakeClient{})
		dir := legacyProfile(t, sw, "1", "", true, true)
		scheduledTick(t)
		if claimed(t, sw, "1") || exists(filepath.Join(dir, ".tycswap-stale-credentials")) {
			t.Fatal("an interrupted drop on the live account did not finish")
		}
	})
	t.Run("verified release", func(t *testing.T) {
		sw := retireStore(t, &oauth.FakeClient{})
		saved := retireCreds("at-2-latest", "rt-2-latest")
		if err := sw.Creds.WriteBackup("2", retireEmails["2"], saved); err != nil {
			t.Fatal(err)
		}
		dir := legacyProfile(t, sw, "2", "", true, false)
		marker := fmt.Sprintf(`{"version":1,"account":{"number":"2","email":%q,"organizationUuid":""},"digest":"%x"}`, retireEmails["2"], sha256.Sum256([]byte(saved)))
		writeTestFile(t, filepath.Join(dir, ".tycswap-release.json"), marker, 0o600)
		scheduledTick(t)
		if claimed(t, sw, "2") || exists(filepath.Join(dir, ".tycswap-release.json")) {
			t.Fatal("an interrupted verified release did not finish")
		}
		if got, _ := sw.ReadAccountCredentials("2", retireEmails["2"]); got != saved {
			t.Fatal("the saved login changed")
		}
	})
}

// A stale profile login lost to a newer stored login is dropped, never saved
// over it, whatever the profile endpoint says about it.
func TestStaleProfileKeepsNewerBackup(t *testing.T) {
	sw := retireStore(t, &oauth.FakeClient{ProfileFn: func(context.Context, string) *oauth.Identity { return &oauth.Identity{Email: "b@example.test"} }})
	dir := legacyProfile(t, sw, "2", retireCreds("at-old-2", "rt-old-2"), true, true)
	if code, out, errb := runCodex(t, "", "switch", "2"); code != 0 {
		t.Fatalf("switch 2 = %d, stdout %q, stderr %q", code, out, errb)
	}
	newer := retireCreds("at-2", "rt-2")
	if got, _ := sw.ReadAccountCredentials("2", retireEmails["2"]); got != newer {
		t.Fatalf("the stale profile login replaced the newer stored one: %q", got)
	}
	if live, _ := os.ReadFile(filepath.Join(sw.Home, ".claude", ".credentials.json")); string(live) != newer {
		t.Fatalf("the switch made %q live", live)
	}
	if claimed(t, sw, "2") || exists(filepath.Join(dir, ".credentials.json")) {
		t.Fatal("the stale profile kept its login or claim")
	}
}

// Guard: a profile a Claude process still runs in keeps everything.
func TestLiveProfileKeepsClaim(t *testing.T) {
	sw := retireStore(t, &oauth.FakeClient{})
	login := retireCreds("at-2", "rt-2")
	dir := legacyProfile(t, sw, "2", login, true, false)
	writeTestFile(t, filepath.Join(dir, "sessions", strconv.Itoa(os.Getpid())+".json"), `{"pid":`+strconv.Itoa(os.Getpid())+`}`, 0o600)
	scheduledTick(t)
	if got, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); !claimed(t, sw, "2") || string(got) != login {
		t.Fatal("a running profile lost its login or claim")
	}
	if exists(filepath.Join(sw.Home, ".claude", "projects", "-work", "s2.jsonl")) {
		t.Fatal("a running profile's conversations were copied")
	}
}
