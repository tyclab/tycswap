// Tests for the Fable store's tick (DESIGN A102).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/wincred"
)

const fableTickNow = "2026-11-18T03:00:00Z"

// fableTickFixture is a store of accounts 1 to 4 whose plans run Fable: the
// default login live on def, Fable logins for logins, the one of live in the
// Fable store ("" leaves it empty), cached usage per account as {5h, 7d,
// Fable} and auto-switch on unless off.
type fableTickFixture struct {
	def, live string
	logins    []string
	usage     map[string][3]float64
	off       bool
	refresh   func(creds string) oauth.RefreshOutcome
}

// refreshes counts the refreshes the fixture's OAuth client was asked for.
type refreshes struct {
	mu    sync.Mutex
	creds []string
}

func (r *refreshes) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.creds...)
}

func fableEmail(num string) string { return "acct" + num + "@example.com" }

func fableLoginOf(num string, expiresAtMS int64) string {
	return `{"claudeAiOauth":{"accessToken":"at-f` + num + `","refreshToken":"rt-f` + num + `","expiresAt":` + strconv.FormatInt(expiresAtMS, 10) + `}}`
}

const farExpiry = int64(4102444800000)

func (fx fableTickFixture) build(t *testing.T) (*core.Switcher, *refreshes) {
	t.Helper()
	cleanHome(t)
	clk := testutil.FixedClock(t, fableTickNow)
	asked := &refreshes{}
	oc := &oauth.FakeClient{RefreshFn: func(ctx context.Context, creds string) oauth.RefreshOutcome {
		// The client bounds a refresh; the Fable tick's live refresh, its
		// keep-alive and its engine put no deadline of their own on it (DESIGN A129).
		if at, ok := ctx.Deadline(); ok && time.Until(at) < oauth.DefaultRefreshSendTimeout+oauth.DefaultRefreshReplyTimeout {
			t.Errorf("the caller ends the refresh %s from now, before the client's own bounds", time.Until(at).Round(time.Millisecond))
		}
		asked.mu.Lock()
		asked.creds = append(asked.creds, creds)
		asked.mu.Unlock()
		if fx.refresh != nil {
			return fx.refresh(creds)
		}
		return oauth.RefreshOutcome{Error: oauth.ErrTransient}
	}}
	sw, err := core.New(store.Options{Clock: clk, OAuth: oc, Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	sw.Store.Platform = platform.Linux
	sw.Creds = credstore.New(credstore.Config{Platform: platform.Linux, CredentialsDir: sw.CredentialsDir}, sw.Keychain(), clk, sw.Log)
	if err := sw.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	accounts := map[string]json.RawMessage{}
	for _, num := range []string{"1", "2", "3", "4"} {
		email := fableEmail(num)
		accounts[num], _ = json.Marshal(map[string]any{"email": email, "uuid": "u-" + num, "organizationUuid": "", "organizationName": "",
			"subscriptionType": "max", "fableContinue": true, "added": "t"})
		if err := sw.WriteAccountCredentials(num, email, dryCreds("at-d"+num, "rt-d"+num)); err != nil {
			t.Fatal(err)
		}
		if err := sw.WriteAccountConfig(num, email, `{"oauthAccount":{"emailAddress":"`+email+`","organizationUuid":""}}`); err != nil {
			t.Fatal(err)
		}
	}
	active, _ := strconv.Atoi(fx.def)
	if err := sw.WriteSequence(&store.SequenceData{ActiveAccountNumber: &active, LastUpdated: "2026-11-18T02:00:00Z", Sequence: []int{1, 2, 3, 4}, Accounts: accounts}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sw.Home, ".claude.json"), `{"oauthAccount":{"emailAddress":"`+fableEmail(fx.def)+`","organizationUuid":""}}`)
	writeFile(t, filepath.Join(sw.Home, ".claude", ".credentials.json"), dryCreds("at-d"+fx.def, "rt-d"+fx.def))
	f, err := sw.Store.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.logins) > 0 {
		if err := f.SetupFableDirectories(); err != nil {
			t.Fatal(err)
		}
	}
	for _, num := range fx.logins {
		if err := f.WriteAccountCredentials(num, fableEmail(num), fableLoginOf(num, farExpiry)); err != nil {
			t.Fatal(err)
		}
	}
	if fx.live != "" {
		writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"), fableLoginOf(fx.live, farExpiry))
	}
	now := clock.Seconds(clk)
	reset := clk.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	rows := map[string]any{}
	for num, u := range fx.usage {
		rows[num] = map[string]any{"email": fableEmail(num), "organizationUuid": "", "fetchedAt": now - 30, "nextPollAt": now + 600, "pollIntervalS": 600.0,
			"lastGood": map[string]any{
				"five_hour": map[string]any{"pct": u[0], "resets_at": reset},
				"seven_day": map[string]any{"pct": u[1], "resets_at": reset},
				"scoped":    []any{map[string]any{"name": "Fable", "pct": u[2], "resets_at": reset}},
			}}
	}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "accounts": rows})
	writeFile(t, filepath.Join(sw.BackupDir(), "cache", "usage.json"), string(raw))
	if !fx.off {
		if _, err := settings.SetSetting(sw.BackupDir(), "autoswitch.enabled", "true"); err != nil {
			t.Fatal(err)
		}
	}
	useSwitcher(t, sw)
	return sw, asked
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOrAbsent(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "<absent>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// splitByStore parses the JSON lines of a run into the default tick's and the
// Fable tick's.
func splitByStore(t *testing.T, out string) (def, fable []map[string]any, defLines []string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		ev := decodeJSON(t, line)
		switch ev["store"] {
		case nil:
			def, defLines = append(def, ev), append(defLines, line)
		case "fable":
			fable = append(fable, ev)
		default:
			t.Fatalf("store %v in %s", ev["store"], line)
		}
	}
	return def, fable, defLines
}

func eventOf(evs []map[string]any, kind string) map[string]any {
	for _, ev := range evs {
		if ev["event"] == kind {
			return ev
		}
	}
	return nil
}

func storeLive(t *testing.T, sw *core.Switcher) string {
	t.Helper()
	return readOrAbsent(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"))
}

func fableStatePath(sw *core.Switcher) string {
	return filepath.Join(paths.FableScopeDir(sw.BackupDir()), "autoswitch_state.json")
}

// atFableLimit: the default login on 1 below every bar, the Fable store on 2
// at its Fable limit, 3 with Fable room and a Fable login.
var atFableLimit = fableTickFixture{def: "1", live: "2", logins: []string{"2", "3"},
	usage: map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 100}, "3": {10, 20, 10}, "4": {10, 20, 100}}}

// The Fable tick runs after the default one and moves the Fable store to 3;
// the default tick decides as before and the roster is untouched.
func TestFableTickSwitchesTheFableStore(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	seq := readOrAbsent(t, sw.SequenceFile)
	defCreds := readOrAbsent(t, filepath.Join(sw.Home, ".claude", ".credentials.json"))
	code, out, errb := runCodex(t, "", "auto", "--once", "--json")
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout %q stderr %q", code, out, errb)
	}
	def, fable, _ := splitByStore(t, out)
	if ns := eventOf(def, "no-switch"); ns == nil || ns["reason"] != "below-threshold" || eventOf(def, "switch") != nil {
		t.Fatalf("default tick: %v", def)
	}
	sw2 := eventOf(fable, "switch")
	from, _ := sw2["from"].(map[string]any)
	to, _ := sw2["to"].(map[string]any)
	if sw2 == nil || from["number"] != float64(2) || to["number"] != float64(3) || sw2["trigger"] != "at-limit" {
		t.Fatalf("Fable tick: %v", fable)
	}
	if got := storeLive(t, sw); !strings.Contains(got, "at-f3") {
		t.Fatalf("Fable store holds %s, want Account-3's Fable login", got)
	}
	if readOrAbsent(t, sw.SequenceFile) != seq || readOrAbsent(t, filepath.Join(sw.Home, ".claude", ".credentials.json")) != defCreds {
		t.Fatal("the roster or the default login changed")
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(readOrAbsent(t, fableStatePath(sw))), &state); err != nil || state["lastSwitchTo"] != "3" || state["lastSwitchBy"] != nil {
		t.Fatalf("Fable state %v (%v)", state, err)
	}
	log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log"))
	if !strings.Contains(log, "Switched the Fable store to account 3 by engine:at-limit") || strings.Contains(log, "Switched to account 3") {
		t.Fatalf("log:\n%s", log)
	}
}

// No Fable login, or the Fable store off: the run is the one tick of
// v0.15.1, byte for byte (plan 1.1). With the store off it keeps an idle
// Fable login alive all the same, and says nothing of it on stdout (F2-9);
// without a Fable login it asks for no refresh and creates no Fable directory.
func TestFableTickNoChangeWithoutAStore(t *testing.T) {
	base := atFableLimit
	base.live, base.logins = "", nil
	base.refresh = func(creds string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Credentials: strings.Replace(creds, `"rt-f`, `"rt-new-f`, 1)}
	}
	run := func(fx fableTickFixture, idle string) (int, string, string) {
		sw, asked := fx.build(t)
		if idle != "" {
			f, _ := sw.Store.ForFable()
			at := sw.Clk.Now().Add(-20 * 24 * time.Hour).Add(fableTokenLife).UnixMilli()
			if err := f.WriteAccountCredentials(idle, fableEmail(idle), fableLoginOf(idle, at)); err != nil {
				t.Fatal(err)
			}
		}
		code, out, _ := runCodex(t, "", "auto", "--once", "--json")
		if _, err := os.Stat(fableStatePath(sw)); !os.IsNotExist(err) {
			t.Fatalf("the Fable state file exists (%v)", err)
		}
		if fx.logins == nil {
			for _, dir := range []string{paths.FableCredentialsDir(sw.BackupDir()), paths.FableScopeDir(sw.BackupDir())} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("%s exists without a Fable login (%v)", dir, err)
				}
			}
		}
		return code, out, refreshedSlots(asked)
	}
	codeNone, none, refreshedNone := run(base, "")
	off := base
	off.logins = []string{"2", "3"}
	codeOff, outOff, refreshedOff := run(off, "")
	codeIdle, outIdle, refreshedIdle := run(off, "3")
	if codeNone != 2 || codeOff != codeNone || outOff != none || strings.Contains(none, `"store"`) || codeIdle != codeNone || outIdle != none {
		t.Fatalf("no login: exit %d\n%s\nstore off: exit %d\n%s\nstore off, an idle login: exit %d\n%s", codeNone, none, codeOff, outOff, codeIdle, outIdle)
	}
	if refreshedNone != "" || refreshedOff != "" || refreshedIdle != "3" {
		t.Fatalf("refreshed: no login %q, store off %q, store off with Account-3 idle %q (want 3)", refreshedNone, refreshedOff, refreshedIdle)
	}
	if record := os.Getenv("TYCSWAP_FABLETICK_RECORD"); record != "" {
		if err := os.WriteFile(filepath.Clean(record), []byte(none), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Auto-switch off: the default tick's disabled event only, exit 2, and the
// Fable state is not touched.
func TestFableTickOffSaysDisabledOnce(t *testing.T) {
	fx := atFableLimit
	fx.off = true
	sw, _ := fx.build(t)
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 2 || len(lines) != 1 || !strings.Contains(lines[0], `"reason":"disabled"`) || strings.Contains(out, `"store"`) {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	if got := readOrAbsent(t, fableStatePath(sw)); got != "<absent>" {
		t.Fatalf("Fable state written while off: %s", got)
	}
}

// Auto-switch off: no Fable tick runs, yet a real run stores a login Claude
// Code rotated in the Fable store, as every read does for the default login.
// The output and the Fable state stay as an off run leaves them, and a dry run
// stores nothing.
func TestAutoSwitchOffStillStoresTheFableStoresRotatedLogin(t *testing.T) {
	fx := atFableLimit
	fx.off = true
	sw, _ := fx.build(t)
	sw.Store.OAuth.(*oauth.FakeClient).ProfileFn = func(_ context.Context, token string) *oauth.Identity {
		if token != "at-f2-rotated" {
			return nil
		}
		return &oauth.Identity{UUID: "u-2", Email: fableEmail("2")}
	}
	writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"at-f2-rotated","refreshToken":"rt-f2-rotated","expiresAt":4102444800000}}`)
	f, _ := sw.Store.ForFable()
	backup := func() string { c, _ := f.ReadAccountCredentials("2", fableEmail("2")); return c }
	runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	if got := backup(); strings.Contains(got, "rotated") {
		t.Fatalf("a dry run stored the rotated login: %s", got)
	}
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 2 || len(lines) != 1 || !strings.Contains(lines[0], `"reason":"disabled"`) || strings.Contains(out, `"store"`) {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	if got := backup(); !strings.Contains(got, "rt-f2-rotated") {
		t.Fatalf("auto-switch off: the Fable store's rotated login was not stored: %s", got)
	}
	if got := readOrAbsent(t, fableStatePath(sw)); got != "<absent>" {
		t.Fatalf("Fable state written while off: %s", got)
	}
	if got := storeLive(t, sw); !strings.Contains(got, "at-f2-rotated") {
		t.Fatalf("the Fable store's live login changed: %s", got)
	}
}

// Auto-switch off on a box without a Fable login: a real run asks no profile
// and creates nothing of the Fable scope.
func TestAutoSwitchOffWithoutAFableLoginCreatesNothing(t *testing.T) {
	sw, _ := fableTickFixture{def: "1", off: true, usage: map[string][3]float64{"1": {10, 20, 30}}}.build(t)
	asked := 0
	sw.Store.OAuth.(*oauth.FakeClient).ProfileFn = func(context.Context, string) *oauth.Identity {
		asked++
		return nil
	}
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	if code != 2 || !strings.Contains(out, `"reason":"disabled"`) {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	for _, dir := range []string{paths.FableScopeDir(sw.BackupDir()), paths.FableCredentialsDir(sw.BackupDir())} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s exists after an off run without a Fable login (%v)", dir, err)
		}
	}
	if asked != 0 {
		t.Fatalf("an off run without a Fable login asked the profile endpoint %d times", asked)
	}
}

// A dry run decides both ticks and writes neither state file, nor the store.
func TestFableTickDryRunWritesNothing(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	rootState, live := autoswitch.StatePath(sw.BackupDir()), storeLive(t, sw)
	before := readOrAbsent(t, rootState) + readOrAbsent(t, fableStatePath(sw))
	code, out, _ := runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	_, fable, _ := splitByStore(t, out)
	if ev := eventOf(fable, "switch"); code != 0 || ev == nil || ev["dryRun"] != true {
		t.Fatalf("exit %d, Fable events %v", code, fable)
	}
	if after := readOrAbsent(t, rootState) + readOrAbsent(t, fableStatePath(sw)); after != before || storeLive(t, sw) != live {
		t.Fatalf("a dry run wrote: before %s, after %s", before, after)
	}
	if _, err := os.Stat(filepath.Join(paths.FableScopeDir(sw.BackupDir()), "live_lineage.json")); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the Fable chain record (%v)", err)
	}
}

// turnOffWhenRefreshing is a refresh that turns auto-switch off in *root
// while it refreshes a credential naming token, and succeeds.
func turnOffWhenRefreshing(t *testing.T, root *string, token string) func(string) oauth.RefreshOutcome {
	return func(creds string) oauth.RefreshOutcome {
		if strings.Contains(creds, token) {
			if _, err := settings.SetSetting(*root, "autoswitch.enabled", "false"); err != nil {
				t.Error(err)
			}
		}
		return oauth.RefreshOutcome{Credentials: creds}
	}
}

// Auto-switch turned off while the default tick freshens its target: the
// Fable tick reads the key at its start and does not run (DESIGN A83, A102).
func TestFableTickDoesNotRunOnceAutoSwitchIsTurnedOff(t *testing.T) {
	var root string
	fx := atFableLimit
	fx.usage = map[string][3]float64{"1": {90, 20, 30}, "2": {10, 20, 100}, "3": {10, 20, 10}, "4": {10, 20, 100}}
	fx.refresh = turnOffWhenRefreshing(t, &root, `"at-d`)
	sw, asked := fx.build(t)
	root = sw.BackupDir()
	near := strconv.FormatInt(int64(clock.Seconds(sw.Clk)*1000+60_000), 10)
	for _, num := range []string{"2", "3", "4"} {
		if err := sw.WriteAccountCredentials(num, fableEmail(num), `{"claudeAiOauth":{"accessToken":"at-d`+num+`","refreshToken":"rt-d`+num+`","expiresAt":`+near+`}}`); err != nil {
			t.Fatal(err)
		}
	}
	_, out, _ := runCodex(t, "", "auto", "--once", "--json")
	def, fable, _ := splitByStore(t, out)
	if len(asked.all()) == 0 || eventOf(def, "switch") != nil {
		t.Fatalf("the default tick freshened %d targets, events %v", len(asked.all()), def)
	}
	if len(fable) != 0 {
		t.Fatalf("the Fable tick ran after auto-switch was turned off: %v", fable)
	}
}

// Auto-switch turned off while the Fable tick freshens its target: the Fable
// engine re-reads the key before it switches, as the default one does.
func TestFableSwitchStopsOnceAutoSwitchIsTurnedOff(t *testing.T) {
	var root string
	fx := atFableLimit
	fx.refresh = turnOffWhenRefreshing(t, &root, `"at-f3"`)
	sw, asked := fx.build(t)
	root = sw.BackupDir()
	f, _ := sw.Store.ForFable()
	if err := f.WriteAccountCredentials("3", fableEmail("3"), fableLoginOf("3", int64(clock.Seconds(sw.Clk)*1000+60_000))); err != nil {
		t.Fatal(err)
	}
	_, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	if ns := eventOf(fable, "no-switch"); len(asked.all()) == 0 || ns == nil || ns["reason"] != "disabled" || eventOf(fable, "switch") != nil {
		t.Fatalf("Fable events %v after %d refreshes", fable, len(asked.all()))
	}
	if got := storeLive(t, sw); !strings.Contains(got, "at-f2") {
		t.Fatalf("Fable store holds %s, want Account-2's Fable login still", got)
	}
}

// A dry run harvests no rotated Fable login; a real tick does.
func TestFableTickDryRunHarvestsNothing(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	sw.Store.OAuth.(*oauth.FakeClient).ProfileFn = func(_ context.Context, token string) *oauth.Identity {
		if token != "at-f3-rotated" {
			return nil
		}
		return &oauth.Identity{UUID: "u-3", Email: fableEmail("3")}
	}
	writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"at-f3-rotated","refreshToken":"rt-f3-rotated","expiresAt":4102444800000}}`)
	f, _ := sw.Store.ForFable()
	backup := func() string { c, _ := f.ReadAccountCredentials("3", fableEmail("3")); return c }
	runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	if got := backup(); strings.Contains(got, "rotated") {
		t.Fatalf("a dry run harvested the rotated login: %s", got)
	}
	runCodex(t, "", "auto", "--once", "--json")
	if got := backup(); !strings.Contains(got, "rt-f3-rotated") {
		t.Fatalf("a real tick did not harvest the rotated login: %s", got)
	}
}

// The Fable engine's poll inputs sit beside the default engine's: the shared
// planner still plans an account on the default engine's model windows.
func TestFablePollInputsKeepTheDefaultEngines(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	now := clock.Seconds(sw.Clk)
	at := func(d time.Duration) string { return sw.Clk.Now().Add(d).UTC().Format(time.RFC3339) }
	rows := map[string]any{"3": map[string]any{"email": fableEmail("3"), "organizationUuid": "", "fetchedAt": now - 30, "nextPollAt": now + 300, "pollIntervalS": 600.0,
		"lastGood": map[string]any{
			"five_hour": map[string]any{"pct": 10.0, "resets_at": at(3 * time.Hour)},
			"seven_day": map[string]any{"pct": 20.0, "resets_at": at(3 * time.Hour)},
			"scoped": []any{map[string]any{"name": "Opus", "pct": 50.0, "resets_at": at(2 * time.Minute)},
				map[string]any{"name": "Fable", "pct": 10.0, "resets_at": at(3 * time.Hour)}},
		}}}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "accounts": rows})
	writeFile(t, filepath.Join(sw.BackupDir(), "cache", "usage.json"), string(raw))
	root := sw.BackupDir()
	reporting.SetScopedPollInputs(root, root, 90, []string{"Opus"})
	t.Cleanup(func() { reporting.ClearScopedPollInputs(root, root) })
	a, _ := newFableAdapter(sw, sw.Log)
	a.SetPollPolicyInputs(90, []string{"Fable"})
	t.Cleanup(a.ClearPollPolicyInputs)
	if next := a.UsageEntriesByAccount(map[string]bool{})["3"].NextPollAt; next == nil || *next >= now+300 {
		t.Fatalf("Account-3's next poll was not brought before %v by its Opus window", now+300)
	}
}

// The Fable engine fetches usage with the default login, the one fetch path
// both stores share (DESIGN A102), never with an account's Fable login.
func TestFableUsageFetchUsesTheDefaultLogin(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	var tokens []string
	sw.Store.OAuth.(*oauth.FakeClient).UsageFn = func(_ context.Context, token string) (map[string]any, error) {
		tokens = append(tokens, token)
		return map[string]any{"five_hour": map[string]any{"utilization": 10.0}, "seven_day": map[string]any{"utilization": 20.0}}, nil
	}
	rows := map[string]any{"3": map[string]any{"email": fableEmail("3"), "organizationUuid": "", "fetchedAt": clock.Seconds(sw.Clk) - 4000}}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "accounts": rows})
	writeFile(t, filepath.Join(sw.BackupDir(), "cache", "usage.json"), string(raw))
	a, _ := newFableAdapter(sw, sw.Log)
	a.UsageEntriesByAccount(map[string]bool{"3": true})
	if strings.Join(tokens, ",") != "at-d3" {
		t.Fatalf("usage fetched with %q, want Account-3's default login at-d3", tokens)
	}
}

// A live Fable login no account's Fable login or chain record names is not
// the default login's account: the Fable tick leaves it, as the default tick
// leaves an unmanaged login.
func TestFableTickLeavesAnUnknownLiveLogin(t *testing.T) {
	fx := atFableLimit
	fx.usage = map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 100}, "3": {10, 20, 10}, "4": {10, 20, 100}}
	sw, _ := fx.build(t)
	unknown := fableLoginOf("9", farExpiry)
	writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"), unknown)
	_, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	if ns := eventOf(fable, "no-switch"); ns == nil || ns["reason"] != "unmanaged-active-account" || eventOf(fable, "switch") != nil {
		t.Fatalf("Fable events %v", fable)
	}
	if storeLive(t, sw) != unknown {
		t.Fatal("the Fable tick replaced the unknown live login")
	}
}

// A login made elsewhere (a /login in a Fable-store session: a new grant only
// the profile endpoint knows) is reported as the default tick reports one,
// although resolving it writes the Fable chain record; a switch of the Fable
// store by hand is not (DESIGN A102, F2-5).
func TestFableTickReportsALoginMadeElsewhere(t *testing.T) {
	for _, byHand := range []bool{false, true} {
		t.Run(map[bool]string{false: "elsewhere", true: "by-hand"}[byHand], func(t *testing.T) {
			fx := fableTickFixture{def: "3", live: "1", logins: []string{"1", "2"},
				usage: map[string][3]float64{"1": {10, 20, 10}, "2": {10, 20, 10}, "3": {10, 20, 30}, "4": {10, 20, 30}}}
			sw, _ := fx.build(t)
			if code, out, _ := runCodex(t, "", "auto", "--once", "--json"); code != 2 {
				t.Fatalf("tick 1 exit %d: %s", code, out)
			}
			var state map[string]any
			if err := json.Unmarshal([]byte(readOrAbsent(t, fableStatePath(sw))), &state); err != nil || state["lastSeenLive"] != "1" {
				t.Fatalf("after tick 1: %v (%v)", state, err)
			}
			// Two minutes pass; the fixture's clock stands still.
			state["lastSeenAt"] = state["lastSeenAt"].(float64) - 120
			raw, _ := json.Marshal(state)
			writeFile(t, fableStatePath(sw), string(raw))
			if byHand {
				if r, err := sw.SwitchFable("2", true); err != nil || r["switched"] != true {
					t.Fatalf("hand switch: %v, %v", r, err)
				}
			} else {
				writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"),
					`{"claudeAiOauth":{"accessToken":"at-elsewhere","refreshToken":"rt-elsewhere","expiresAt":4102444800000}}`)
				sw.Store.OAuth = &oauth.FakeClient{ProfileFn: func(_ context.Context, token string) *oauth.Identity {
					if token == "at-elsewhere" {
						return &oauth.Identity{Email: fableEmail("2"), UUID: "u-2"}
					}
					return nil
				}}
			}
			_, out, _ := runCodex(t, "", "auto", "--once", "--json")
			_, fable, _ := splitByStore(t, out)
			changed := eventOf(fable, "live-login-changed")
			log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log"))
			if byHand {
				if changed != nil || strings.Contains(log, "without a tycswap switch") {
					t.Fatalf("a switch by hand reported as a login made elsewhere: %v", fable)
				}
				return
			}
			if changed == nil || changed["from"] != "1" || changed["to"] != "2" {
				t.Fatalf("Fable events %v, want live-login-changed from 1 to 2", fable)
			}
			if !strings.Contains(log, "Fable store: Live login changed from account 1 to 2 without a tycswap switch") {
				t.Fatalf("log:\n%s", log)
			}
		})
	}
}

// move, swap and an add --slot migration of the Fable store's account change
// its number, not its login: the next Fable tick reports no login made
// elsewhere, since the Fable engine state follows the renumbering.
func TestFableTickReportsNoLoginChangeAfterARenumbering(t *testing.T) {
	for _, tc := range []struct {
		name, to string
		act      func(t *testing.T, sw *core.Switcher)
	}{
		{"move", "5", func(t *testing.T, sw *core.Switcher) {
			if code, out, errOut := runCodex(t, "", "move", "1", "5"); code != 0 {
				t.Fatalf("move exit %d: %s%s", code, out, errOut)
			}
		}},
		{"swap", "2", func(t *testing.T, sw *core.Switcher) {
			if code, out, errOut := runCodex(t, "", "swap", "1", "2"); code != 0 {
				t.Fatalf("swap exit %d: %s%s", code, out, errOut)
			}
		}},
		{"add --slot", "5", func(t *testing.T, sw *core.Switcher) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".claude.json"), `{"oauthAccount":{"emailAddress":"`+fableEmail("1")+`","organizationUuid":""}}`)
			writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("at-n1", "rt-n1"))
			slot, prev := 5, lifecycle.Output
			lifecycle.Output = &bytes.Buffer{}
			t.Cleanup(func() { lifecycle.Output = prev })
			if _, err := sw.AddAccountFromLogin(lifecycle.LoginDir(dir, nil), &slot, true, nil); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := fableTickFixture{def: "3", live: "1", logins: []string{"1", "2"},
				usage: map[string][3]float64{"1": {10, 20, 10}, "2": {10, 20, 10}, "3": {10, 20, 30}, "4": {10, 20, 30}}}
			sw, _ := fx.build(t)
			if code, out, _ := runCodex(t, "", "auto", "--once", "--json"); code != 2 {
				t.Fatalf("tick 1 exit %d: %s", code, out)
			}
			var state map[string]any
			if err := json.Unmarshal([]byte(readOrAbsent(t, fableStatePath(sw))), &state); err != nil || state["lastSeenLive"] != "1" {
				t.Fatalf("after tick 1: %v (%v)", state, err)
			}
			state["lastSeenAt"] = state["lastSeenAt"].(float64) - 120
			raw, _ := json.Marshal(state)
			writeFile(t, fableStatePath(sw), string(raw))
			tc.act(t, sw)
			_, out, _ := runCodex(t, "", "auto", "--once", "--json")
			_, fable, _ := splitByStore(t, out)
			if changed := eventOf(fable, "live-login-changed"); changed != nil {
				t.Fatalf("a renumbering reported as a login made elsewhere: %v", changed)
			}
			if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); strings.Contains(log, "without a tycswap switch") {
				t.Fatalf("log:\n%s", log)
			}
			if err := json.Unmarshal([]byte(readOrAbsent(t, fableStatePath(sw))), &state); err != nil || state["lastSeenLive"] != tc.to {
				t.Fatalf("after tick 2: lastSeenLive %v, want %s (%v)", state["lastSeenLive"], tc.to, err)
			}
		})
	}
}

// Every Fable event carries "store":"fable" and its line names the store;
// the default tick's lines are those of a run without the Fable store.
func TestFableEventsCarryTheStore(t *testing.T) {
	atFableLimit.build(t)
	_, out, _ := runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	_, fable, defLines := splitByStore(t, out)
	if len(fable) == 0 {
		t.Fatalf("no Fable event: %s", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, `"store"`) && !regexp.MustCompile(`^\{.*"event":"[a-z-]+".*"store":"fable"`).MatchString(line) {
			t.Fatalf("Fable line %s", line)
		}
	}
	off := atFableLimit
	off.live = ""
	off.build(t)
	_, plain, _ := runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	if strings.Join(defLines, "\n") != strings.TrimSpace(plain) {
		t.Fatalf("default lines\n%s\nwithout the Fable store\n%s", strings.Join(defLines, "\n"), plain)
	}
	atFableLimit.build(t)
	_, human, _ := runCodex(t, "", "auto", "--once", "--dry-run")
	if n := strings.Count(human, "  Fable store: "); n != len(fable) {
		t.Fatalf("%d human lines name the Fable store, want %d:\n%s", n, len(fable), human)
	}
}

// A switch of the Fable store by hand holds the Fable tick for the cooldown
// and not the default tick (A94 per store).
func TestFableHandSwitchHoldsTheFableTickOnly(t *testing.T) {
	fx := fableTickFixture{def: "1", live: "3", logins: []string{"2", "3"},
		usage: map[string][3]float64{"1": {90, 20, 30}, "2": {10, 20, 100}, "3": {10, 20, 10}, "4": {10, 20, 100}}}
	sw, _ := fx.build(t)
	if r, err := sw.SwitchFable("2", true); err != nil || r["switched"] != true {
		t.Fatalf("hand switch: %v, %v", r, err)
	}
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	def, fable, _ := splitByStore(t, out)
	held := eventOf(fable, "no-switch")
	if code != 0 || eventOf(def, "switch") == nil || held == nil || held["reason"] != "cooldown" ||
		held["detail"] != "switched by hand to account 2; held for another 300s" || eventOf(fable, "switch") != nil {
		t.Fatalf("exit %d\ndefault %v\nFable %v", code, def, fable)
	}
}

// A switch of the default login by hand holds only the default tick.
func TestDefaultHandSwitchHoldsTheDefaultTickOnly(t *testing.T) {
	fx := fableTickFixture{def: "4", live: "2", logins: []string{"2", "3"},
		usage: map[string][3]float64{"1": {90, 20, 30}, "2": {10, 20, 100}, "3": {10, 20, 10}, "4": {10, 20, 100}}}
	sw, _ := fx.build(t)
	if _, err := sw.SwitchTo("1", true); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	def, fable, _ := splitByStore(t, out)
	if held := eventOf(def, "no-switch"); code != 0 || held == nil || held["reason"] != "cooldown" || eventOf(fable, "switch") == nil {
		t.Fatalf("exit %d\ndefault %v\nFable %v", code, def, fable)
	}
}

// The default login at its limit moves to another account reserved for the
// Fable store before the Fable store's own account (F2-3).
func TestDefaultTickTakesTheFableStoresAccountLast(t *testing.T) {
	fx := fableTickFixture{def: "3", live: "1", logins: []string{"1", "2"},
		usage: map[string][3]float64{"1": {10, 20, 10}, "2": {10, 30, 10}, "3": {10, 100, 100}, "4": {10, 100, 100}}}
	sw, _ := fx.build(t)
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	def, _, _ := splitByStore(t, out)
	ev := eventOf(def, "switch")
	to, _ := ev["to"].(map[string]any)
	if code != 0 || ev == nil || ev["trigger"] != "at-limit" || to["number"] != float64(2) {
		t.Fatalf("exit %d, default events %v; want the at-limit move to Account-2", code, def)
	}
	if got := readOrAbsent(t, filepath.Join(sw.Home, ".claude", ".credentials.json")); !strings.Contains(got, "at-d2") || !strings.Contains(storeLive(t, sw), "at-f1") {
		t.Fatal("the default login is not on Account-2 beside the Fable store on Account-1")
	}
}

// A default login on the Fable store's account hands over to another reserved
// account below every bar, under the cooldown, and stays there: three ticks,
// no move back (F2-3).
func TestDefaultLoginLeavesTheFableStoresAccount(t *testing.T) {
	fx := fableTickFixture{def: "1", live: "1", logins: []string{"1", "2"},
		usage: map[string][3]float64{"1": {10, 20, 10}, "2": {10, 30, 10}, "3": {10, 100, 100}, "4": {10, 100, 100}}}
	sw, _ := fx.build(t)
	defLive := func() string { return readOrAbsent(t, filepath.Join(sw.Home, ".claude", ".credentials.json")) }
	tick := func() []map[string]any {
		_, out, _ := runCodex(t, "", "auto", "--once", "--json")
		def, fable, _ := splitByStore(t, out)
		if eventOf(fable, "switch") != nil || !strings.Contains(storeLive(t, sw), "at-f1") {
			t.Fatalf("the Fable store moved: %v", fable)
		}
		return def
	}
	def := tick()
	ev := eventOf(def, "switch")
	to, _ := ev["to"].(map[string]any)
	if ev == nil || ev["trigger"] != "reserved" || to["number"] != float64(2) || !strings.Contains(defLive(), "at-d2") {
		t.Fatalf("tick 1: %v; want the reserved hand-over to Account-2", def)
	}
	if ns := eventOf(tick(), "no-switch"); ns == nil || ns["reason"] != "cooldown" || !strings.Contains(defLive(), "at-d2") {
		t.Fatalf("tick 2, within the cooldown: %v", ns)
	}
	statePath := autoswitch.StatePath(sw.BackupDir())
	var state map[string]any
	if err := json.Unmarshal([]byte(readOrAbsent(t, statePath)), &state); err != nil {
		t.Fatal(err)
	}
	state["lastSwitchAt"] = state["lastSwitchAt"].(float64) - 3600
	raw, _ := json.Marshal(state)
	writeFile(t, statePath, string(raw))
	if ns := eventOf(tick(), "no-switch"); ns == nil || ns["reason"] != "reserved-no-target" || !strings.Contains(defLive(), "at-d2") {
		t.Fatalf("tick 3, after the cooldown: %v; want Account-2 kept", ns)
	}
}

// A dead Fable login is quarantined in the Fable state file only; the next
// Fable tick skips it, and a new Fable login releases it.
func TestDeadFableLoginIsTheFableStoresOnly(t *testing.T) {
	dead := false
	fx := atFableLimit
	fx.refresh = func(string) oauth.RefreshOutcome {
		if dead {
			return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
		}
		return oauth.RefreshOutcome{Error: oauth.ErrTransient}
	}
	sw, asked := fx.build(t)
	f, _ := sw.Store.ForFable()
	near := clock.Seconds(sw.Clk)*1000 + 60_000
	if err := f.WriteAccountCredentials("3", fableEmail("3"), fableLoginOf("3", int64(near))); err != nil {
		t.Fatal(err)
	}
	runCodex(t, "", "auto", "--once", "--json")
	rootState := readOrAbsent(t, autoswitch.StatePath(sw.BackupDir()))

	dead = true
	code, out, _ := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	if q := eventOf(fable, "account-quarantined"); code != 3 || q == nil || q["reason"] != "invalid_grant" {
		t.Fatalf("exit %d, Fable events %v", code, fable)
	}
	if got := readOrAbsent(t, autoswitch.StatePath(sw.BackupDir())); got != rootState {
		t.Fatalf("root state changed:\n%s\nwas\n%s", got, rootState)
	}
	if entries := autoswitch.ReadQuarantineEntries(fableStatePath(sw)); entries["3"].Reason != "invalid_grant" {
		t.Fatalf("Fable quarantine %v", entries)
	}

	n := len(asked.all())
	_, out, _ = runCodex(t, "", "auto", "--once", "--json")
	if _, fable, _ = splitByStore(t, out); len(asked.all()) != n || eventOf(fable, "switch") != nil || eventOf(fable, "account-quarantined") != nil {
		t.Fatalf("the next tick tried 3 again: %v", fable)
	}

	if err := f.WriteAccountCredentials("3", fableEmail("3"), `{"claudeAiOauth":{"accessToken":"at-f3b","refreshToken":"rt-f3b","expiresAt":4102444800000}}`); err != nil {
		t.Fatal(err)
	}
	_, out, _ = runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ = splitByStore(t, out)
	if r := eventOf(fable, "account-unquarantined"); r == nil || r["reason"] != "credentials-replaced" || eventOf(fable, "switch") == nil {
		t.Fatalf("after a new Fable login: %v", fable)
	}
}

// The adapter answers for the Fable store over the default store's roster.
func TestFableAdapterMethods(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	a, err := newFableAdapter(sw, sw.Log)
	if err != nil {
		t.Fatal(err)
	}
	if cur := a.CurrentAccountNumber(); cur == nil || *cur != "2" || !a.HasLiveLogin() {
		t.Fatalf("live %v, has login %v", cur, a.HasLiveLogin())
	}
	if got := strings.Join(a.SwitchableAccountNumbers(), ","); got != "2,3" {
		t.Fatalf("pool %s, want 2,3", got)
	}
	if a.AccountEmail("3") != fableEmail("3") || a.AccountKindFor("3") != "oauth" || a.AccountIdentity("3")["uuid"] != "u-3" || a.BackupDir() != sw.BackupDir() {
		t.Fatal("roster answers are not the default store's")
	}
	if got := a.ReadAccountCredentials("3", fableEmail("3")); !strings.Contains(got, "at-f3") {
		t.Fatalf("credentials %s, want the Fable login", got)
	}
	if err := a.PersistBackupCredentials("3", fableEmail("3"), fableLoginOf("3", farExpiry-1)); err != nil {
		t.Fatal(err)
	}
	if got, _ := sw.ReadAccountCredentials("3", fableEmail("3")); strings.Contains(got, "at-f3") {
		t.Fatal("the default login was written")
	}
	if a.LiveSessionPidsFor("3", fableEmail("3")) != nil {
		t.Fatal("a Fable login has no session owner")
	}
	if out := a.RefreshBackupGuarded(context.Background(), sw.OAuth, "2", fableEmail("2"), fableLoginOf("2", farExpiry)); out.Error != store.RefreshDeclined {
		t.Fatalf("refresh of the live Fable login: %+v, want declined", out)
	}
	a.SetPollPolicyInputs(80, []string{"Fable"})
	t.Cleanup(a.ClearPollPolicyInputs)
	r, err := a.SwitchTo("3", true)
	if err != nil || r["switched"] != true || r["store"] != "fable" {
		t.Fatalf("switch %v, %v", r, err)
	}
	// The switch alone records nothing: the switch stamp its caller writes does.
	if slot, at := a.RecordedSwitch(); slot != "" || at != 0 {
		t.Fatalf("recorded switch %s at %v before a stamp", slot, at)
	}
	if err := autoswitch.HandSwitch(a.fable.StatePath(), sw.Clk, nil, func() (string, error) { return "3", nil }); err != nil {
		t.Fatal(err)
	}
	if slot, at := a.RecordedSwitch(); slot != "3" || at != clock.Seconds(sw.Clk) {
		t.Fatalf("recorded switch %s at %v", slot, at)
	}
	if entries := a.UsageEntriesByAccount(map[string]bool{}); entries["3"].LastGood == nil {
		t.Fatal("usage is not the default store's")
	}
	if _, ok := any(a).(interface{ PrepareRotationCandidates() }); ok {
		t.Fatal("the adapter prepares rotation candidates")
	}
	if _, ok := any(a).(interface{ PrepareRotationTarget(string) error }); ok {
		t.Fatal("the adapter prepares rotation targets")
	}
	if _, ok := any(a).(interface{ RotationBlockReasons() map[string]string }); ok {
		t.Fatal("the adapter reports ownership blocks")
	}
}

// The engine's write-back of a refreshed Fable login waits for the store
// lock another process holds.
func TestFableAdapterPersistsUnderTheStoreLock(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	a, err := newFableAdapter(sw, sw.Log)
	if err != nil {
		t.Fatal(err)
	}
	other := filelock.New(sw.Store.LockFile, 0)
	if ok, err := other.Acquire(0); !ok || err != nil {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	t.Cleanup(func() { _ = other.Release() })
	fresh := fableLoginOf("3", farExpiry-1)
	done := make(chan error, 1)
	go func() { done <- a.PersistBackupCredentials("3", fableEmail("3"), fresh) }()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if a.ReadAccountCredentials("3", fableEmail("3")) == fresh {
			t.Fatal("written while another process held the store lock")
		}
	}
	_ = other.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := a.ReadAccountCredentials("3", fableEmail("3")); got != fresh {
		t.Fatalf("Fable login = %s", got)
	}
}

// The pool leaves out, in turn, a disabled account, a plan without Fable, an
// account without a Fable login and one quarantined in the Fable state file;
// the default store's quarantine leaves it in.
func TestFableAdapterPool(t *testing.T) {
	quarantined := func(path func(*core.Switcher) string) func(*testing.T, *core.Switcher) {
		return func(t *testing.T, sw *core.Switcher) {
			writeFile(t, path(sw), `{"schemaVersion":1,"quarantine":{"3":{"email":"acct3@example.com","reason":"invalid_grant"}}}`)
		}
	}
	for name, tc := range map[string]struct {
		logins  []string
		prepare func(*testing.T, *core.Switcher)
		want    string
	}{
		"all":      {[]string{"2", "3", "4"}, nil, "2,3,4"},
		"disabled": {[]string{"2", "3", "4"}, func(t *testing.T, sw *core.Switcher) { _ = sw.SetAccountDisabled("3", true) }, "2,4"},
		"plan-without-fable": {[]string{"2", "3", "4"}, func(t *testing.T, sw *core.Switcher) {
			setRecord(t, sw, "3", "subscriptionType", "pro", "fableContinue", false)
		}, "2,4"},
		"no-fable-login":     {[]string{"2", "4"}, nil, "2,4"},
		"fable-quarantine":   {[]string{"2", "3", "4"}, quarantined(fableStatePath), "2,4"},
		"default-quarantine": {[]string{"2", "3", "4"}, quarantined(func(sw *core.Switcher) string { return autoswitch.StatePath(sw.BackupDir()) }), "2,3,4"},
	} {
		t.Run(name, func(t *testing.T) {
			fx := atFableLimit
			fx.logins = tc.logins
			sw, _ := fx.build(t)
			if tc.prepare != nil {
				tc.prepare(t, sw)
			}
			a, _ := newFableAdapter(sw, sw.Log)
			if got := strings.Join(a.SwitchableAccountNumbers(), ","); got != tc.want {
				t.Fatalf("pool %s, want %s", got, tc.want)
			}
		})
	}
}

func setRecord(t *testing.T, sw *core.Switcher, num string, kv ...any) {
	t.Helper()
	data, err := sw.SequenceForUpdate()
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(data.Accounts[num], &rec); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		rec[kv[i].(string)] = kv[i+1]
	}
	data.Accounts[num], _ = json.Marshal(rec)
	if err := sw.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
}

// keepAliveFixture: the Fable store on 2, with Fable room; Fable logins of
// 1, 3 and 4 last refreshed ago[num] ago (2's 30 days ago).
func keepAliveFixture(t *testing.T, ago map[string]time.Duration, refresh func(string) oauth.RefreshOutcome) (*core.Switcher, *refreshes) {
	t.Helper()
	fx := atFableLimit
	fx.logins, fx.refresh = []string{"1", "2", "3", "4"}, refresh
	fx.usage = map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 10}, "3": {10, 20, 10}, "4": {10, 20, 100}}
	sw, asked := fx.build(t)
	f, _ := sw.Store.ForFable()
	ago["2"] = 30 * 24 * time.Hour
	for num, d := range ago {
		at := sw.Clk.Now().Add(-d).Add(fableTokenLife).UnixMilli()
		if err := f.WriteAccountCredentials(num, fableEmail(num), fableLoginOf(num, at)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"), fableLoginOf("2", sw.Clk.Now().Add(-30*24*time.Hour).Add(fableTokenLife).UnixMilli()))
	return sw, asked
}

func refreshedSlots(asked *refreshes) string {
	var out []string
	for _, c := range asked.all() {
		for _, num := range []string{"1", "2", "3", "4"} {
			if strings.Contains(c, `"at-f`+num+`"`) {
				out = append(out, num)
			}
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// One real Fable tick keeps the idle login refreshed longest ago alive, and
// only it: not a fresh one, never the live one.
func TestKeepAliveRefreshesTheOldestIdleLogin(t *testing.T) {
	renewed := func(creds string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Credentials: strings.Replace(creds, `"rt-f`, `"rt-new-f`, 1)}
	}
	sw, asked := keepAliveFixture(t, map[string]time.Duration{"1": 20 * 24 * time.Hour, "3": 16 * 24 * time.Hour, "4": 24 * time.Hour}, renewed)
	a, _ := newFableAdapter(sw, sw.Log)
	keepFableLoginAlive(a, sw, func(autoswitch.Event) {})
	if got := refreshedSlots(asked); got != "1" {
		t.Fatalf("refreshed %q, want 1", got)
	}
	if got := a.ReadAccountCredentials("1", fableEmail("1")); !strings.Contains(got, "rt-new-f1") {
		t.Fatalf("Account-1's Fable login %s", got)
	}
	if log := readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")); !strings.Contains(log, "INFO") || !strings.Contains(log, "Kept the Fable login of account 1 alive") {
		t.Fatalf("log:\n%s", log)
	}

	sw, asked = keepAliveFixture(t, map[string]time.Duration{"1": 13 * 24 * time.Hour, "3": time.Hour}, renewed)
	a, _ = newFableAdapter(sw, sw.Log)
	keepFableLoginAlive(a, sw, func(autoswitch.Event) {})
	if got := refreshedSlots(asked); got != "" {
		t.Fatalf("refreshed %q; fresh logins and the live one are left alone", got)
	}
}

// A dead idle login is quarantined in the Fable state file.
func TestKeepAliveQuarantinesADeadLogin(t *testing.T) {
	sw, _ := keepAliveFixture(t, map[string]time.Duration{"3": 20 * 24 * time.Hour}, func(string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
	})
	a, _ := newFableAdapter(sw, sw.Log)
	var got []autoswitch.Event
	keepFableLoginAlive(a, sw, func(ev autoswitch.Event) { got = append(got, ev) })
	if entries := autoswitch.ReadQuarantineEntries(fableStatePath(sw)); entries["3"].Reason != "invalid_grant" || len(got) != 1 || got[0].Kind() != "account-quarantined" {
		t.Fatalf("quarantine %v, events %v", entries, got)
	}
	if _, err := os.Stat(autoswitch.StatePath(sw.BackupDir())); !os.IsNotExist(err) {
		t.Fatalf("the root state file was written (%v)", err)
	}
}

// A Fable login's quarantine line names the Fable sign-in, not the default
// login's re-add; the default store's line is as before (S1).
func TestFableQuarantineLineNamesTheFableSignIn(t *testing.T) {
	// The live login on 2 has expired and its refresh is refused.
	keepAliveFixture(t, map[string]time.Duration{}, func(string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
	})
	_, out, _ := runCodex(t, "", "auto", "--once")
	want := "Fable store: Account-2 (acct2@example.com) quarantined: invalid_grant. Sign its Fable login in again with 'tycswap add --login --lineage fable --slot 2'."
	if !strings.Contains(out, want) || strings.Contains(out, "--add-account") {
		t.Fatalf("stdout lacks %q:\n%s", want, out)
	}
	def := autoswitch.QuarantineEvent{Number: "3", Email: "acct3@example.com", Reason: "invalid_grant"}.Human()
	if def != "Account-3 (acct3@example.com) quarantined: invalid_grant. Log in with it and run 'tycswap --add-account --slot 3' to recover." {
		t.Fatalf("default line %q", def)
	}
}

// A quarantined idle login is left: the oldest idle login not quarantined is
// the one kept alive.
func TestKeepAliveLeavesAQuarantinedLogin(t *testing.T) {
	sw, asked := keepAliveFixture(t, map[string]time.Duration{"1": 20 * 24 * time.Hour, "3": 16 * 24 * time.Hour}, func(creds string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Credentials: creds}
	})
	if err := autoswitch.Quarantine(fableStatePath(sw), sw.Clk, "1", fableEmail("1"), oauth.ErrInvalidGrant, "", func(autoswitch.Event) {}); err != nil {
		t.Fatal(err)
	}
	a, _ := newFableAdapter(sw, sw.Log)
	keepFableLoginAlive(a, sw, func(autoswitch.Event) {})
	if got := refreshedSlots(asked); got != "3" {
		t.Fatalf("refreshed %q, want 3; the quarantined 1 is left", got)
	}
}

// The Fable store on Account-1 by its last install only (its login rotated
// and not yet attributed): the keep-alive leaves 1, Claude Code's, and keeps
// the oldest of the other idle logins alive (DESIGN A100, A102).
func TestKeepAliveSkipsTheFableStoresLastInstall(t *testing.T) {
	sw, asked := keepAliveFixture(t, map[string]time.Duration{"1": 40 * 24 * time.Hour, "3": 20 * 24 * time.Hour, "4": 24 * time.Hour}, func(creds string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Credentials: creds}
	})
	f, _ := sw.Store.ForFable()
	held, _ := f.ReadAccountCredentials("1", fableEmail("1"))
	if err := sw.Store.RecordFableLive("1", held); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"at-rotated","refreshToken":"rt-rotated","expiresAt":4102444800000}}`)
	data, _ := sw.ReadSequence()
	if got := f.ResolveOwnerSlot(data); got.Slot != "1" || got.Source != store.SourceLastInstall {
		t.Fatalf("the store's owner %+v, want Account-1 by last install", got)
	}
	a, _ := newFableAdapter(sw, sw.Log)
	keepFableLoginAlive(a, sw, func(autoswitch.Event) {})
	if got := refreshedSlots(asked); got != "2" {
		t.Fatalf("refreshed %q, want 2 (the oldest idle login but the store's account 1)", got)
	}
}

// A dry run keeps nothing alive; a real tick does, and checks the expired
// live login too.
func TestKeepAliveOnlyOnARealTick(t *testing.T) {
	_, asked := keepAliveFixture(t, map[string]time.Duration{"3": 20 * 24 * time.Hour}, func(creds string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Credentials: creds}
	})
	runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	if got := refreshedSlots(asked); got != "" {
		t.Fatalf("a dry run refreshed %q", got)
	}
	runCodex(t, "", "auto", "--once", "--json")
	if got := refreshedSlots(asked); got != "2,3" {
		t.Fatalf("a real tick refreshed %q, want 2 (the live login, expired) and 3", got)
	}
}

// A Fable-store session's status line records its store and, near a bar,
// makes the Fable store's live account due, not the default login's.
func TestObserveInAFableSession(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	prev := fableSessionProcess
	fableSessionProcess = true
	t.Cleanup(func() { fableSessionProcess = prev })
	raw := []byte(`{"session_id":"s-f","model":{"id":"claude-fable-5-1"},"rate_limits":{"five_hour":{"used_percentage":99}}}`)
	if !observeTicks(sw, raw, settings.Default(), true) {
		t.Fatal("no tick near the bar")
	}
	if family, st, ok := reporting.RecordedSession(sw.BackupDir(), "s-f"); !ok || family != "fable" || st != "fable" {
		t.Fatalf("record %q, %q, %v", family, st, ok)
	}
	ids := map[string]usage.Identity{"1": {Email: fableEmail("1")}, "2": {Email: fableEmail("2")}}
	entries, now := sw.Usage.Entries(ids), clock.Seconds(sw.Clk)
	if entries["2"].NextPollAt == nil || *entries["2"].NextPollAt > now || *entries["1"].NextPollAt <= now {
		t.Fatalf("due: Fable live %v, default %v, now %v", *entries["2"].NextPollAt, *entries["1"].NextPollAt, now)
	}
}

// A default-store status line records no store and marks the default
// login's account due, as before.
func TestObserveInADefaultSession(t *testing.T) {
	sw, _ := atFableLimit.build(t)
	raw := []byte(`{"session_id":"s-d","model":{"id":"claude-opus-5-5"},"rate_limits":{"five_hour":{"used_percentage":99}}}`)
	observeTicks(sw, raw, settings.Default(), true)
	if _, st, ok := reporting.RecordedSession(sw.BackupDir(), "s-d"); !ok || st != "" {
		t.Fatalf("record store %q, %v", st, ok)
	}
	ids := map[string]usage.Identity{"1": {Email: fableEmail("1")}, "2": {Email: fableEmail("2")}}
	entries, now := sw.Usage.Entries(ids), clock.Seconds(sw.Clk)
	if *entries["1"].NextPollAt > now || *entries["2"].NextPollAt <= now {
		t.Fatalf("due: default %v, Fable live %v, now %v", *entries["1"].NextPollAt, *entries["2"].NextPollAt, now)
	}
	raw2, _ := os.ReadFile(filepath.Join(sw.BackupDir(), "default_models.json"))
	if strings.Contains(string(raw2), `"store"`) {
		t.Fatalf("default record %s", raw2)
	}
}

func TestSignificantOutcome(t *testing.T) {
	E, S, B, N := autoswitch.Error, autoswitch.Switched, autoswitch.Blocked, autoswitch.NoAction
	for _, tc := range [][3]autoswitch.TickOutcome{{N, N, N}, {N, B, B}, {B, S, S}, {S, E, E}, {E, N, E}, {S, N, S}, {B, N, B}} {
		if got := significantOutcome(tc[0], tc[1]); got != tc[2] {
			t.Errorf("significantOutcome(%d, %d) = %d, want %d", tc[0], tc[1], got, tc[2])
		}
	}
}
