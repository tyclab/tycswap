package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

func TestParkedFableRefillsOnlyForEligibleFreshNativeStart(t *testing.T) {
	fx := fableTickFixture{def: "4", live: "1", logins: []string{"1", "2", "3"}, off: true,
		usage: map[string][3]float64{"1": {10, 10, 10}, "2": {10, 10, 100}, "3": {10, 10, 10}}}
	sw, rec := fableStartLogin(t, fx, "1", "2")
	startResolve = func(string) (string, error) { return "/verified/claude", nil }
	defaultBefore := defaultLive(t, sw)
	run := func(code int, args ...string) string {
		t.Helper()
		got, _, msg := runCLI(t, args, false, false)
		if got != code {
			t.Fatalf("%v: exit %d want %d: %s", args, got, code, msg)
		}
		return msg
	}
	parked := func(want bool) {
		t.Helper()
		got, err := store.FableAllocationParked(sw.BackupDir())
		if err != nil || got != want {
			t.Fatalf("parked = %t,%v; want %t", got, err, want)
		}
	}
	run(0, "models", "1", "opus")
	parked(true)
	if got := storeLive(t, sw); got != "<absent>" {
		t.Fatalf("automatic parking kept login: %s", got)
	}
	if msg := run(1, "start", "--", "claude", "--model", "fable"); !strings.Contains(msg, "no eligible") || rec.execs != 0 {
		t.Fatalf("out of quota: %s, execs=%d", msg, rec.execs)
	}
	usageFile := filepath.Join(sw.BackupDir(), "cache", "usage.json")
	b, err := os.ReadFile(usageFile)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, usageFile, strings.ReplaceAll(string(b), `"pct":100`, `"pct":20`))
	if msg := run(1, "start", "--", "claude", "--model", "fable", "--continue"); !strings.Contains(msg, "original login") {
		t.Fatalf("unbound continuation: %s", msg)
	}
	run(23, "start", "--", "claude", "--model", "fable")
	parked(false)
	digest := envValue(rec.env, recoveredFableEnv)
	if !validMythosDigest(digest) || envValue(rec.env, secureStorageVar) != fableStoreDir() || rec.execs != 1 || !strings.Contains(storeLive(t, sw), "at-f2") {
		t.Fatalf("native recovery did not select eligible Fable-only login: execs=%d", rec.execs)
	}
	r, err := store.ReadFableReaders(sw.BackupDir())
	if err != nil || r.Identity != digest || len(r.PIDs) != 1 || r.PIDs[0] != os.Getpid() {
		t.Fatalf("reader: %+v, %v", r, err)
	}
	if _, err := sw.EngineSwitchFable("3", true); err == nil {
		t.Fatal("timer moved the recovered process")
	}
	if _, err := sw.SwitchFable("3", true); err == nil {
		t.Fatal("manual switch moved the recovered process")
	}
	if _, err := sw.EmptyFable(true); err == nil {
		t.Fatal("manual empty removed the recovered login")
	}
	if defaultLive(t, sw) != defaultBefore || !strings.Contains(storeLive(t, sw), "at-f2") {
		t.Fatal("runtime protection changed the live accounts")
	}

	const session = "19ee7961-7069-4e80-93e6-e92b36920010"
	t.Setenv(secureStorageEnv, fableStoreDir())
	t.Setenv(recoveredFableEnv, digest)
	t.Setenv(startFamilyEnv, "fable")
	if code, _, msg := runCodex(t, `{"source":"startup","session_id":"`+session+`","model":"fable"}`, "start", "--started"); code != 0 {
		t.Fatal(msg)
	}
	if _, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), "other", "opus", "", time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if binding, err := reporting.SessionBinding(sw.BackupDir(), session); err != nil || binding != "fable/"+digest {
		t.Fatalf("delayed binding = %q, %v", binding, err)
	}
	if family, scope, ok := reporting.RecordedSession(sw.BackupDir(), session); !ok || family != "fable" || scope != "fable" {
		t.Fatalf("dashboard scope = %q/%q", family, scope)
	}
	run(23, "start", "--", "claude", "--model", "fable", "--resume", session)
	if rec.execs != 2 || envValue(rec.env, recoveredFableEnv) != digest {
		t.Fatal("resume changed its original identity")
	}

	// Exercise a stale PID using a child that the operating system has reaped.
	cmd := exec.Command(os.Args[0], "-test.run", "^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	r.PIDs = []int{cmd.Process.Pid}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(paths.FableScopeDir(sw.BackupDir()), "recovered-readers.json"), string(raw))
	setRecord(t, sw, "3", "startLogin", true)
	rawUsage, err := os.ReadFile(usageFile)
	if err != nil {
		t.Fatal(err)
	}
	var cached struct {
		Accounts      map[string]map[string]any `json:"accounts"`
		SchemaVersion int                       `json:"schemaVersion"`
	}
	if err := json.Unmarshal(rawUsage, &cached); err != nil {
		t.Fatal(err)
	}
	cached.Accounts["2"]["lastGood"].(map[string]any)["scoped"].([]any)[0].(map[string]any)["pct"] = 100
	changed, err := json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, usageFile, string(changed))
	run(23, "start", "--", "claude", "--model", "fable")
	if rec.execs != 3 || !strings.Contains(storeLive(t, sw), "at-f3") {
		t.Fatal("a completed recovered process pinned its exhausted account forever")
	}
	run(1, "start", "--", "claude", "--model", "fable", "--resume", session)
	if rec.execs != 3 || !strings.Contains(storeLive(t, sw), "at-f3") {
		t.Fatal("bound resume followed a later login")
	}

}

func TestParkedFableExplicitEmptyStaysOff(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "4", live: "1", logins: []string{"1", "2"}, off: true}, "1", "2")
	startResolve = func(string) (string, error) { return "/verified/claude", nil }
	for _, args := range [][]string{{"models", "1", "opus"}, {"switch", "--store", "fable", "--empty"}} {
		if code, _, msg := runCLI(t, args, false, false); code != 0 {
			t.Fatalf("%v: %s", args, msg)
		}
	}
	parked, err := store.FableAllocationParked(sw.BackupDir())
	if err != nil || parked {
		t.Fatalf("manual off: %v/%v", parked, err)
	}
	code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false)
	if code != 23 || rec.execs != 1 || envValue(rec.env, secureStorageVar) != "" || storeLive(t, sw) != "<absent>" {
		t.Fatalf("manual off: %d, %s", code, msg)
	}
}

func TestFableReallocationWaitsForObservedUsers(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "4", live: "1", logins: []string{"1", "2"}, off: true}, "1", "2")
	startResolve = func(string) (string, error) { return "/verified/claude", nil }
	before := storeLive(t, sw)
	pid := os.Getpid()
	writeFile(t, filepath.Join(sw.DefaultProfileDir(), "sessions", fmt.Sprint(pid)+".json"), fmt.Sprintf(`{"pid":%d,"sessionId":"unknown-session"}`, pid))
	if code, _, msg := runCLI(t, []string{"models", "1", "opus"}, false, false); code != 0 {
		t.Fatal(msg)
	}
	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false); code != 1 || !strings.Contains(msg, "still in use") {
		t.Fatalf("live reader: %d, %s", code, msg)
	}
	if rec.execs != 0 || storeLive(t, sw) != before {
		t.Fatal("existing client's credential was changed")
	}
}

func TestFableUnmarkParksAndContinuationAloneCannotRefill(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "4", live: "1", logins: []string{"1", "2"}, off: true}, "1", "2")
	startResolve = func(string) (string, error) { return "/verified/claude", nil }
	for _, n := range []string{"1", "2"} {
		setRecord(t, sw, n, "subscriptionType", "enterprise", store.FableMarkKey, true)
	}
	if code, _, msg := runCLI(t, []string{"fable", "--disallow", "1"}, false, false); code != 0 {
		t.Fatal(msg)
	}
	if parked, err := store.FableAllocationParked(sw.BackupDir()); err != nil || !parked {
		t.Fatalf("parked: %v, %v", parked, err)
	}
	code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false)
	if code != 1 || rec.execs != 0 || !strings.Contains(msg, "no eligible") {
		t.Fatalf("continuation-only grant selected for startup: %d %s", code, msg)
	}
}

func TestParkedFableTickPreparesIdentityWithoutInstalling(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "4", live: "1", logins: []string{"1", "2", "3"}, usage: map[string][3]float64{"2": {10, 10, 10}, "3": {10, 10, 10}}}, "2", "3")
	startResolve = func(string) (string, error) { return "/verified/claude", nil }
	if code, _, msg := runCLI(t, []string{"models", "1", "opus"}, false, false); code != 0 {
		t.Fatal(msg)
	}
	runTick := func(dry bool) { fableTick(sw, settings.Load(sw.BackupDir()), func(autoswitch.Event) {}, dry) }
	state := func(want string) {
		t.Helper()
		p, err := sw.ParkingStatus()
		if err != nil || p == nil || p.ReadyAccount != want || storeLive(t, sw) != "<absent>" {
			t.Fatalf("readiness = %+v, %v; want %q with empty shared credentials", p, err, want)
		}
	}
	runTick(true)
	state("")
	runTick(false)
	state("2")
	for _, args := range [][]string{{"status", "--json"}, {"auto", "status"}, {"auto", "status", "--json"}} {
		if code, out, msg := runCLI(t, args, false, false); code != 0 || !strings.Contains(out, "Account-2 ready") {
			t.Fatalf("%v: %d %s %s", args, code, out, msg)
		}
	}
	if code, _, msg := runCLI(t, []string{"models", "2", "opus"}, false, false); code != 0 {
		t.Fatal(msg)
	}
	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false); code != 23 || rec.execs != 1 || !strings.Contains(storeLive(t, sw), "at-f3") {
		t.Fatalf("stale timer choice won at native start: %d %s", code, msg)
	}
}

func TestRecoveredNativePIDExitsBeforeNextAccountStarts(t *testing.T) {
	if os.Getenv("TYCSWAP_TEST_PARKED_CHILD") == "1" {
		sw, err := core.New(store.Options{Clock: testutil.FixedClock(t, fableTickNow), OAuth: &oauth.FakeClient{}, Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: io.Discard, SkipMigrations: true})
		if err != nil {
			t.Fatal(err)
		}
		sw.Store.Platform = platform.Linux
		useSwitcher(t, sw)
		startResolve = func(string) (string, error) { return "/verified/claude", nil }
		startExec = func(_ string, _ []string, env []string) (int, error) {
			if !validMythosDigest(envValue(env, recoveredFableEnv)) {
				t.Fatal("fake native process did not receive its bound identity")
			}
			return 23, nil
		}
		if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false); code != 23 {
			t.Fatalf("child: %d %s", code, msg)
		}
		return
	}
	sw, rec := fableStartLogin(t, fableTickFixture{def: "4", live: "1", logins: []string{"1", "2", "3"}, off: true, usage: map[string][3]float64{"2": {10, 10, 31}, "3": {10, 10, 45}}}, "2", "3")
	startResolve = func(string) (string, error) { return "/verified/claude", nil }
	if code, _, msg := runCLI(t, []string{"models", "1", "opus"}, false, false); code != 0 {
		t.Fatal(msg)
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestRecoveredNativePIDExitsBeforeNextAccountStarts$")
	cmd.Env = append(os.Environ(), "TYCSWAP_TEST_PARKED_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native start fixture: %s, %v", out, err)
	}
	r, err := store.ReadFableReaders(sw.BackupDir())
	if err != nil || len(r.PIDs) != 1 || r.PIDs[0] != cmd.Process.Pid || r.InUse() || !strings.Contains(storeLive(t, sw), "at-f2") {
		t.Fatalf("completed native reader: %+v, %v", r, err)
	}
	path := filepath.Join(sw.BackupDir(), "cache", "usage.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.ReplaceAll(string(b), `"pct":31`, `"pct":100`))
	if code, _, msg := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false); code != 23 || rec.execs != 1 || !strings.Contains(storeLive(t, sw), "at-f3") {
		t.Fatalf("new fresh native start: %d %s", code, msg)
	}
}
