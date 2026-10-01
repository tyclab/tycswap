// codex_test.go — the `tycswap codex` namespace and the Codex side of `tycswap
// auto`. Port of claude-swap PR #252 tests/test_codex_cli.py (the CLI-layer
// assertions), plus the Go-side deviations: envelope shape, remove on decline,
// the explicit import's schema warning, and the --once tick order.
package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/authfile"
	codexstore "github.com/tyclab/tycswap/internal/codex/store"
	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/testutil"
)

const (
	testAcctA, testUserA = "acct-a", "user-a"
	testAcctB, testUserB = "acct-b", "user-b"
)

// codexHome gives the test a temp HOME, CODEX_HOME and XDG_DATA_HOME, and an
// offline Codex switcher (a FakeClient; codex never "running" unless pids is
// set). usageFn nil means the fake's benign "network" sentinel.
func codexHome(t *testing.T, pids []int, usageFn func(ctx context.Context, at, acct string) api.UsageFetch) string {
	t.Helper()
	cleanHome(t)
	home := os.Getenv("HOME")
	testutil.Setenv(t, "CODEX_HOME", filepath.Join(home, ".codex"))
	testutil.Setenv(t, "XDG_DATA_HOME", filepath.Join(home, "data"))
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := newCodexSwitcher
	newCodexSwitcher = func(s ioStreams) *codexswitcher.Switcher {
		return codexswitcher.New(codexswitcher.Options{
			Platform:    platform.Linux,
			Client:      &api.FakeClient{UsageFn: usageFn},
			Stdout:      s.out,
			Stdin:       s.in,
			RunningPIDs: func() []int { return pids },
		})
	}
	t.Cleanup(func() { newCodexSwitcher = prev })
	// `tycswap auto` sets a process-wide cancel note; later tests expect the default.
	t.Cleanup(func() { setSigintNote("") })
	return home
}

// offlineUsage fails the test on any usage request (test_codex_cli.py offline).
func offlineUsage(t *testing.T) func(context.Context, string, string) api.UsageFetch {
	return func(context.Context, string, string) api.UsageFetch {
		t.Errorf("no network request should be made")
		return api.UsageFetch{Sentinel: api.SentinelNetwork}
	}
}

func makeCodexJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]any{"alg": "none"}) + "." + enc(claims) + ".sig"
}

// makeCodexAuth is conftest_codex.make_auth_json: an auth.json payload whose
// tokens are distinct, recognisable strings.
func makeCodexAuth(t *testing.T, acct, user, email string, exp int64) map[string]any {
	t.Helper()
	claims := map[string]any{
		"exp":   exp,
		"email": email,
		authfile.AuthClaim: map[string]any{
			"chatgpt_account_id": acct,
			"chatgpt_user_id":    user,
			"chatgpt_plan_type":  "pro",
		},
	}
	id := makeCodexJWT(t, claims)
	claims["kind"] = "access"
	access := makeCodexJWT(t, claims)
	return map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  access,
			"refresh_token": "rt-secret-" + acct,
			"account_id":    acct,
		},
		"last_refresh": "2026-08-16T00:00:00Z",
	}
}

func testStore() *codexstore.Store {
	return codexstore.New(codexstore.Options{Platform: platform.Linux})
}

func seedCodex(t *testing.T, acct, user, email string) map[string]any {
	t.Helper()
	st := testStore()
	key := authfile.AccountKey(user, acct)
	if _, err := st.UpsertSlot(key, codexstore.Upsert{Email: email, Plan: "pro"}); err != nil {
		t.Fatal(err)
	}
	payload := makeCodexAuth(t, acct, user, email, time.Now().Unix()+3600)
	if err := st.WriteSnapshot(key, payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func seedOne(t *testing.T) map[string]any {
	return seedCodex(t, testAcctA, testUserA, "a@example.com")
}

func writeLiveAuth(t *testing.T, payload map[string]any) {
	t.Helper()
	b, _ := json.Marshal(payload)
	if err := os.WriteFile(authfile.LiveAuthPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCodex(t *testing.T, stdin string, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run("tycswap", argv, ioStreams{in: strings.NewReader(stdin), out: &out, err: &errb}, false, false)
	return code, out.String(), errb.String()
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("stdout is not one JSON document: %q (%v)", s, err)
	}
	return m
}

// ---- namespace, help ----------------------------------------------------

func TestBareCswapVerbsAreUntouched(t *testing.T) {
	codexHome(t, nil, nil)
	code, out, _ := runCodex(t, "", "--version")
	if code != 0 || !strings.Contains(out, "tycswap") {
		t.Errorf("--version = %d %q", code, out)
	}
}

func TestMainHelpAdvertisesTheCodexNamespace(t *testing.T) {
	var out bytes.Buffer
	renderMainHelp("tycswap", &out)
	help := out.String()
	for _, want := range []string{"Codex (ChatGPT) accounts", "Claude-only", "codex list", "codex status",
		"codex switch", "codex add", "codex login", "codex list --json --skip-api"} {
		if !strings.Contains(help, want) {
			t.Errorf("main help missing %q", want)
		}
	}
	// The codex block sits after the Claude command list, before the aliases.
	if strings.Index(help, "tycswap purge") > strings.Index(help, "Codex (ChatGPT)") ||
		strings.Index(help, "Codex (ChatGPT)") > strings.Index(help, "Aliases:") {
		t.Error("codex block is not between the Claude commands and the aliases line")
	}
}

func TestCodexHelpListsVerbsCaveatAndSettings(t *testing.T) {
	for _, argv := range [][]string{{"codex", "--help"}, {"codex", "-h"}, {"codex", "--debug", "-h"}} {
		code, out, _ := runCodex(t, "", argv...)
		if code != 0 {
			t.Fatalf("%v exit = %d", argv, code)
		}
		for _, want := range []string{"list", "switch", "add", "login", "remove", "alias", "import",
			"ALREADY", "restart", "autoswitch.codexThreshold", "autoswitch.codexEnabled", "--debug"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v help missing %q", argv, want)
			}
		}
	}
}

func TestCodexUsageErrorsExit2(t *testing.T) {
	codexHome(t, nil, nil)
	for _, argv := range [][]string{
		{"codex"},
		{"codex", "frobnicate"},
		{"codex", "switch", "--strategy", "worst"},
		{"codex", "remove"},
		{"codex", "swap", "1"},
		{"codex", "list", "extra"},
		{"codex", "list", "--bogus"},
		{"codex", "add", "--alias"},
	} {
		if code, _, _ := runCodex(t, "", argv...); code != 2 {
			t.Errorf("%v exit = %d, want 2", argv, code)
		}
	}
}

func TestCodexVerbHelp(t *testing.T) {
	code, out, _ := runCodex(t, "", "codex", "switch", "-h")
	if code != 0 || !strings.Contains(out, "usage: tycswap codex switch") {
		t.Errorf("switch -h = %d %q", code, out)
	}
}

func TestCodexAcceptsDebug(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	seedOne(t)
	for _, argv := range [][]string{{"codex", "--debug", "list", "--skip-api"}, {"codex", "list", "--debug", "--skip-api"}} {
		if code, out, errb := runCodex(t, "", argv...); code != 0 || !strings.Contains(out, "a@example.com") {
			t.Errorf("%v = %d out=%q err=%q", argv, code, out, errb)
		}
	}
}

// ---- list ---------------------------------------------------------------

func TestCodexListOnAnEmptyStoreSaysSo(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	_, out, _ := runCodex(t, "", "codex", "list", "--skip-api")
	if !strings.Contains(out, "No Codex accounts") {
		t.Errorf("out = %q", out)
	}
}

func TestCodexListShowsManagedAccounts(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "list", "--skip-api")
	if !strings.Contains(out, "a@example.com") || !strings.Contains(out, "1.") {
		t.Errorf("out = %q", out)
	}
	if strings.HasPrefix(out, "*") {
		t.Errorf("no live login, yet the row is marked active: %q", out)
	}
}

func TestCodexListMarksTheActiveAccount(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	writeLiveAuth(t, seedOne(t))
	_, out, _ := runCodex(t, "", "codex", "list", "--skip-api")
	if !strings.HasPrefix(out, "*") {
		t.Errorf("out = %q, want a leading *", out)
	}
}

func TestCodexListJSONEnvelope(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	writeLiveAuth(t, seedOne(t))
	code, out, errb := runCodex(t, "", "codex", "list", "--json", "--skip-api")
	if code != 0 || errb != "" {
		t.Fatalf("exit %d stderr %q", code, errb)
	}
	data := decodeJSON(t, out)
	if data["schemaVersion"] != float64(1) || data["provider"] != "codex" || data["activeNumber"] != "1" {
		t.Errorf("envelope = %v", data)
	}
	row := data["accounts"].([]any)[0].(map[string]any)
	for _, k := range []string{"number", "email", "workspace", "alias", "active", "disabled", "kind",
		"usage", "sentinel", "fetchedAt", "ageSeconds", "plan"} {
		if _, ok := row[k]; !ok {
			t.Errorf("row missing %q: %v", k, row)
		}
	}
	if row["email"] != "a@example.com" || row["number"] != "1" || row["active"] != true {
		t.Errorf("row = %v", row)
	}
	if _, ok := data["tokenStatus"]; ok {
		t.Error("tokenStatus present without --token-status")
	}
}

func TestCodexListJSONReportsAgeAndFetchTime(t *testing.T) {
	codexHome(t, nil, func(context.Context, string, string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 3.0}, "plan": "pro"}}
	})
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "list", "--json")
	row := decodeJSON(t, out)["accounts"].([]any)[0].(map[string]any)
	if row["fetchedAt"] == nil || row["ageSeconds"] == nil || row["plan"] != "pro" {
		t.Errorf("row = %v", row)
	}
	u, _ := row["usage"].(map[string]any)
	if u == nil {
		t.Fatalf("usage = %v", row["usage"])
	}
	if _, raw := u["five_hour"]; raw {
		t.Errorf("usage carries the raw snake_case dict: %v", u)
	}
}

func TestCodexListHumanUsageSummary(t *testing.T) {
	codexHome(t, nil, func(context.Context, string, string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 42.0}, "plan": "pro"}}
	})
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "list")
	if !strings.Contains(out, "5h 42%") {
		t.Errorf("out = %q", out)
	}
}

func TestCodexTokenStatusNeverPrintsTheToken(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	payload := seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "list", "--skip-api", "--token-status")
	if !strings.Contains(out, "token:") || !strings.Contains(out, "expires in") {
		t.Errorf("out = %q", out)
	}
	for _, k := range []string{"access_token", "refresh_token", "id_token"} {
		if secret := payload["tokens"].(map[string]any)[k].(string); strings.Contains(out, secret) {
			t.Errorf("output leaks %s", k)
		}
	}
}

func TestCodexTokenStatusJSONCarriesTheBlock(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "list", "--json", "--skip-api", "--token-status")
	entry := decodeJSON(t, out)["tokenStatus"].([]any)[0].(map[string]any)
	if entry["state"] != "oauth" || entry["hasRefreshToken"] != true || entry["refreshDue"] != false {
		t.Errorf("entry = %v", entry)
	}
	if strings.Contains(out, "access_token") || strings.Contains(out, "rt-secret") {
		t.Error("token material in the JSON")
	}
}

func TestCodexTokenStatusMarksAnExpiredTokenAsDue(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	st := testStore()
	key := authfile.AccountKey(testUserA, testAcctA)
	st.UpsertSlot(key, codexstore.Upsert{Email: "a@example.com", Plan: "pro"})
	st.WriteSnapshot(key, makeCodexAuth(t, testAcctA, testUserA, "a@example.com", time.Now().Unix()-3600))
	_, out, _ := runCodex(t, "", "codex", "list", "--skip-api", "--token-status")
	if !strings.Contains(out, "refresh due") || !strings.Contains(out, "expired") {
		t.Errorf("out = %q", out)
	}
}

func TestCodexTokenStatusOfAnAPIKeyAccount(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	st := testStore()
	key := authfile.AccountKey(testUserA, testAcctA)
	st.UpsertSlot(key, codexstore.Upsert{Email: "a@example.com", AuthMode: "apikey"})
	st.WriteSnapshot(key, map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x", "tokens": nil})
	_, out, _ := runCodex(t, "", "codex", "list", "--skip-api", "--token-status")
	if !strings.Contains(out, "token: api key") {
		t.Errorf("out = %q", out)
	}
}

func TestCodexRelative(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		in   *float64
		want string
	}{{nil, "unknown"}, {f(0), "expired"}, {f(-5), "expired"}, {f(720), "12m"},
		{f(3*3600 + 12*60), "3h 12m"}, {f(6*86400 + 4*3600), "6d 4h"}} {
		if got := codexRelative(tc.in); got != tc.want {
			t.Errorf("codexRelative(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---- auto-import --------------------------------------------------------

func writeRegistry(t *testing.T, schema int, withSnapshot bool) {
	t.Helper()
	key := authfile.AccountKey(testUserA, testAcctA)
	dir := authfile.AuthAccountsDir()
	os.MkdirAll(dir, 0o700)
	reg := map[string]any{
		"schema_version":     schema,
		"active_account_key": key,
		"accounts": []any{map[string]any{
			"account_key": key, "chatgpt_account_id": testAcctA, "chatgpt_user_id": testUserA,
			"email": "a@example.com", "alias": "", "account_name": nil, "plan": "pro", "auth_mode": "chatgpt",
		}},
	}
	b, _ := json.Marshal(reg)
	os.WriteFile(authfile.AuthRegistryPath(), b, 0o600)
	if withSnapshot {
		b, _ = json.Marshal(makeCodexAuth(t, testAcctA, testUserA, "a@example.com", time.Now().Unix()+3600))
		os.WriteFile(filepath.Join(dir, authfile.FileKey(key)+".auth.json"), b, 0o600)
	}
}

func TestFirstCodexCommandImportsCodexAuthAccounts(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	writeRegistry(t, 3, true)
	_, out, _ := runCodex(t, "", "codex", "list", "--skip-api")
	if !strings.Contains(out, "Imported 1") || !strings.Contains(out, "a@example.com") {
		t.Errorf("out = %q", out)
	}
}

func TestTheImportNoticeStaysOffStdoutUnderJSON(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	writeRegistry(t, 3, true)
	_, out, errb := runCodex(t, "", "codex", "list", "--json", "--skip-api")
	decodeJSON(t, out)
	if !strings.Contains(errb, "Imported 1") {
		t.Errorf("stderr = %q, want the import notice", errb)
	}
}

func TestTheImportDoesNotRepeatOnTheNextCommand(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	seedOne(t)
	writeRegistry(t, 3, true)
	_, out, _ := runCodex(t, "", "codex", "list", "--skip-api")
	if strings.Contains(out, "Imported") {
		t.Errorf("out = %q", out)
	}
}

func TestExplicitCodexAuthImportReportsItsCounts(t *testing.T) {
	codexHome(t, nil, nil)
	_, out, _ := runCodex(t, "", "codex", "import-codex-auth")
	if !strings.Contains(out, "Imported 0") {
		t.Errorf("out = %q", out)
	}
}

func TestExplicitCodexAuthImportWarnsOnAnUnsupportedSchema(t *testing.T) {
	codexHome(t, nil, nil)
	writeRegistry(t, 99, true)
	code, out, _ := runCodex(t, "", "codex", "import-codex-auth")
	if code != 0 || !strings.Contains(out, "uses schema 99") || !strings.Contains(out, "Imported 0, skipped 0.") {
		t.Errorf("exit %d out = %q", code, out)
	}
	// The automatic import warns the same way.
	_, out, _ = runCodex(t, "", "codex", "list", "--skip-api")
	if !strings.Contains(out, "uses schema 99") {
		t.Errorf("auto import out = %q", out)
	}
}

// ---- status -------------------------------------------------------------

func TestCodexStatusReportsTheLiveAccount(t *testing.T) {
	codexHome(t, nil, nil)
	writeLiveAuth(t, seedOne(t))
	_, out, _ := runCodex(t, "", "codex", "status")
	if !strings.Contains(out, "a@example.com") {
		t.Errorf("out = %q", out)
	}
}

func TestCodexStatusWithoutAManagedLoginSaysSo(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "status")
	if !strings.Contains(out, "No active Codex account") {
		t.Errorf("out = %q", out)
	}
}

func TestCodexStatusJSONEnvelope(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "status", "--json")
	data := decodeJSON(t, out)
	if v, ok := data["active"]; !ok || v != nil || data["schemaVersion"] != float64(1) || data["provider"] != "codex" {
		t.Errorf("no-active status = %v", data)
	}

	writeLiveAuth(t, makeCodexAuth(t, testAcctA, testUserA, "a@example.com", time.Now().Unix()+3600))
	_, out, _ = runCodex(t, "", "codex", "status", "--json")
	data = decodeJSON(t, out)
	active, _ := data["active"].(map[string]any)
	if active == nil || active["number"] != float64(1) || active["email"] != "a@example.com" {
		t.Errorf("active = %v", data["active"])
	}
	if data["totalManagedAccounts"] != float64(1) {
		t.Errorf("totalManagedAccounts = %v", data["totalManagedAccounts"])
	}
}

// ---- switch -------------------------------------------------------------

func TestSwitchPrintsARestartWarningWhenCodexIsRunning(t *testing.T) {
	codexHome(t, []int{4242}, nil)
	seedOne(t)
	code, out, _ := runCodex(t, "", "codex", "switch", "1")
	if code != 0 || !strings.Contains(out, "Switched to Codex account 1: a@example.com") {
		t.Fatalf("exit %d out %q", code, out)
	}
	if !strings.Contains(strings.ToLower(out), "restart") || !strings.Contains(out, "4242") {
		t.Errorf("out = %q", out)
	}
}

func TestSwitchIsQuietWhenNothingIsRunning(t *testing.T) {
	codexHome(t, []int{}, nil)
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "switch", "1")
	if strings.Contains(strings.ToLower(out), "restart") {
		t.Errorf("out = %q", out)
	}
}

func TestBareSwitchRotates(t *testing.T) {
	codexHome(t, nil, nil)
	writeLiveAuth(t, seedOne(t))
	seedCodex(t, testAcctB, testUserB, "b@example.com")
	code, out, errb := runCodex(t, "", "codex", "switch")
	if code != 0 || !strings.Contains(out, "Switched to Codex account 2: b@example.com") {
		t.Errorf("exit %d out %q err %q", code, out, errb)
	}
}

// ---- mutations ----------------------------------------------------------

func TestAliasCanBeSetAndCleared(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "alias", "a@example.com", "work")
	if testStore().Slots()[0].Alias != "work" || !strings.Contains(out, "Codex account 1 is now 'work'") {
		t.Errorf("out = %q slots = %v", out, testStore().Slots())
	}
	_, out, _ = runCodex(t, "", "codex", "alias", "1", "--unset")
	if testStore().Slots()[0].Alias != "" || !strings.Contains(out, "Cleared alias for Codex account 1") {
		t.Errorf("out = %q", out)
	}
}

func TestAnInvalidAliasIsACleanError(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	code, _, errb := runCodex(t, "", "codex", "alias", "1", "has space")
	if code != 1 || !strings.Contains(strings.ToLower(errb), "alias") {
		t.Errorf("exit %d stderr %q", code, errb)
	}
}

func TestDisableAndEnableRoundTripNamingTheSlot(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	_, out, _ := runCodex(t, "", "codex", "disable", "a@example.com")
	if !testStore().Slots()[0].Disabled || !strings.Contains(out, "Codex account 1 disabled") {
		t.Errorf("out = %q", out)
	}
	_, out, _ = runCodex(t, "", "codex", "enable", "A@EXAMPLE.COM")
	if testStore().Slots()[0].Disabled || !strings.Contains(out, "Codex account 1 enabled") {
		t.Errorf("out = %q", out)
	}
}

func TestRemoveForgetsTheAccount(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	code, out, _ := runCodex(t, "", "codex", "remove", "a@example.com", "-y")
	if code != 0 || len(testStore().Slots()) != 0 || !strings.Contains(out, "Removed Codex account 1") {
		t.Errorf("exit %d out %q", code, out)
	}
}

func TestRemoveDeclinedPrintsOnlyCancelled(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	code, out, _ := runCodex(t, "n\n", "codex", "remove", "1")
	if code != 0 || !strings.Contains(out, "Cancelled") || strings.Contains(out, "Removed") {
		t.Errorf("exit %d out %q", code, out)
	}
	if len(testStore().Slots()) != 1 {
		t.Error("a declined remove removed the account")
	}
}

func TestSwapAndMove(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	seedCodex(t, testAcctB, testUserB, "b@example.com")
	if _, out, _ := runCodex(t, "", "codex", "swap", "1", "2"); !strings.Contains(out, "Swapped Codex slots 1 and 2") {
		t.Errorf("swap out = %q", out)
	}
	if _, out, _ := runCodex(t, "", "codex", "move", "1", "5"); !strings.Contains(out, "Moved Codex account 1 to slot 5") ||
		strings.Contains(out, "swapped") {
		t.Errorf("move out = %q", out)
	}
	if _, out, _ := runCodex(t, "", "codex", "move", "5", "2"); !strings.Contains(out, "(swapped with its occupant)") {
		t.Errorf("move onto a taken slot out = %q", out)
	}
	if _, out, _ := runCodex(t, "", "codex", "move", "2", "2"); !strings.Contains(out, "already in slot 2") {
		t.Errorf("no-op move out = %q", out)
	}
}

func TestExportImportPurge(t *testing.T) {
	home := codexHome(t, nil, nil)
	seedOne(t)
	path := filepath.Join(home, "codex-export.json")
	if code, out, errb := runCodex(t, "", "codex", "export", path); code != 0 ||
		!strings.Contains(out, "Exported 1 Codex account(s) to "+path) {
		t.Fatalf("export = %d %q %q", code, out, errb)
	}
	code, out, _ := runCodex(t, "", "codex", "export", "-")
	if code != 0 || decodeJSON(t, out)["provider"] != "codex" || strings.Contains(out, "Exported") {
		t.Errorf("export - = %d %q", code, out)
	}
	if code, out, _ := runCodex(t, "", "codex", "purge", "-y"); code != 0 || !strings.Contains(out, "Removed 1 Codex account(s)") {
		t.Errorf("purge = %d %q", code, out)
	}
	if code, out, errb := runCodex(t, "", "codex", "import", path); code != 0 || !strings.Contains(out, "Imported 1 Codex account(s)") {
		t.Errorf("import = %d %q %q", code, out, errb)
	}
	if len(testStore().Slots()) != 1 {
		t.Error("import did not restore the account")
	}
}

func TestPurgeOfAnEmptyStore(t *testing.T) {
	codexHome(t, nil, nil)
	_, out, _ := runCodex(t, "", "codex", "purge", "-y")
	if !strings.Contains(out, "No tycswap Codex data to remove.") {
		t.Errorf("out = %q", out)
	}
}

func TestExportOfAnEmptyStoreIsATransferError(t *testing.T) {
	home := codexHome(t, nil, nil)
	code, _, errb := runCodex(t, "", "codex", "export", filepath.Join(home, "x.json"))
	if code != 1 || !strings.Contains(errb, "No Codex accounts") {
		t.Errorf("exit %d stderr %q", code, errb)
	}
}

// TestEveryVerbReportsAnUnknownAccount: each verb that names an account exits 1
// with the resolver's message.
func TestEveryVerbReportsAnUnknownAccount(t *testing.T) {
	home := codexHome(t, nil, nil)
	seedOne(t)
	for _, argv := range [][]string{
		{"codex", "switch", "99"},
		{"codex", "remove", "99", "-y"},
		{"codex", "alias", "99", "work"},
		{"codex", "alias", "99", "--unset"},
		{"codex", "disable", "99"},
		{"codex", "enable", "nobody@example.com"},
		{"codex", "swap", "1", "99"},
		{"codex", "move", "99", "2"},
		{"codex", "export", filepath.Join(home, "one.json"), "--account", "99"},
	} {
		code, _, errb := runCodex(t, "", argv...)
		if code != 1 || !strings.Contains(errb, "No Codex account matches") {
			t.Errorf("%v: exit %d stderr %q", argv, code, errb)
		}
	}
}

func TestErrorsUnderJSONUseTheEnvelope(t *testing.T) {
	codexHome(t, nil, nil)
	os.MkdirAll(filepath.Dir(authfile.AuthRegistryPath()), 0o700)
	// A store that cannot be written (the store root is a file) fails the
	// automatic import; under --json that is the error envelope on stdout.
	os.MkdirAll(paths.GetBackupRoot(), 0o700)
	os.WriteFile(filepath.Join(paths.GetBackupRoot(), "codex"), []byte("x"), 0o600)
	writeRegistry(t, 3, true)
	code, out, _ := runCodex(t, "", "codex", "list", "--json", "--skip-api")
	if code != 1 {
		t.Skipf("store write did not fail on this platform (exit %d)", code)
	}
	env := decodeJSON(t, out)
	if env["schemaVersion"] != float64(1) || env["error"] == nil {
		t.Errorf("envelope = %v", env)
	}
}

// ---- login --------------------------------------------------------------

func stubLogin(t *testing.T, path string, lookErr error, code int, onRun func(bin string, args []string)) {
	t.Helper()
	prevLook, prevRun := codexLookPath, runCodexLogin
	codexLookPath = func(string) (string, error) { return path, lookErr }
	runCodexLogin = func(bin string, args []string, _ ioStreams) (int, error) {
		onRun(bin, args)
		return code, nil
	}
	t.Cleanup(func() { codexLookPath, runCodexLogin = prevLook, prevRun })
}

func TestCodexLoginRunsTheResolvedBinaryAndStoresTheAccount(t *testing.T) {
	codexHome(t, nil, nil)
	var gotBin string
	var gotArgs []string
	stubLogin(t, "/usr/local/bin/codex", nil, 0, func(bin string, args []string) {
		gotBin, gotArgs = bin, args
		writeLiveAuth(t, makeCodexAuth(t, testAcctA, testUserA, "a@example.com", time.Now().Unix()+3600))
	})
	code, out, errb := runCodex(t, "", "codex", "login", "--device-auth", "--alias", "work")
	if code != 0 || !strings.Contains(out, "Added Codex account 1: a@example.com") {
		t.Fatalf("exit %d out %q err %q", code, out, errb)
	}
	if gotBin != "/usr/local/bin/codex" || len(gotArgs) != 2 || gotArgs[0] != "login" || gotArgs[1] != "--device-auth" {
		t.Errorf("ran %q %v", gotBin, gotArgs)
	}
	slots := testStore().Slots()
	if len(slots) != 1 || slots[0].AccountKey != authfile.AccountKey(testUserA, testAcctA) || slots[0].Alias != "work" {
		t.Errorf("slots = %v", slots)
	}
}

func TestCodexLoginWithoutTheBinaryFailsClearly(t *testing.T) {
	codexHome(t, nil, nil)
	stubLogin(t, "", errors.New("not found"), 0, func(string, []string) { t.Error("ran without a binary") })
	code, _, errb := runCodex(t, "", "codex", "login")
	if code != 1 || !strings.Contains(strings.ToLower(errb), "codex") {
		t.Errorf("exit %d stderr %q", code, errb)
	}
}

func TestAFailedCodexLoginDoesNotAddAnAccount(t *testing.T) {
	codexHome(t, nil, nil)
	stubLogin(t, "/usr/local/bin/codex", nil, 2, func(string, []string) {})
	code, _, errb := runCodex(t, "", "codex", "login")
	if code != 2 || !strings.Contains(errb, "codex login did not complete.") {
		t.Errorf("exit %d stderr %q", code, errb)
	}
	if len(testStore().Slots()) != 0 {
		t.Error("a failed login added an account")
	}
}

func TestAddWithoutALiveLoginFails(t *testing.T) {
	codexHome(t, nil, nil)
	code, _, errb := runCodex(t, "", "codex", "add")
	if code != 1 || !strings.Contains(errb, "No Codex login found") {
		t.Errorf("exit %d stderr %q", code, errb)
	}
}

// ---- tycswap auto ---------------------------------------------------------

func TestAutoOnceRunsTheCodexTickAfterTheClaudeTick(t *testing.T) {
	codexHome(t, nil, func(context.Context, string, string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 10.0}, "plan": "pro"}}
	})
	writeLiveAuth(t, seedOne(t))
	code, out, errb := runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := decodeJSON(t, lines[len(lines)-1])
	if last["event"] != "codex" {
		t.Fatalf("last line is not the codex event: %q (stderr %q)", out, errb)
	}
	for _, k := range []string{"schemaVersion", "ts", "outcome", "detail", "switchedTo", "runningPids"} {
		if _, ok := last[k]; !ok {
			t.Errorf("codex event missing %q: %v", k, last)
		}
	}
	if last["outcome"] != "ok" || last["switchedTo"] != nil {
		t.Errorf("codex event = %v", last)
	}
	for _, l := range lines[:len(lines)-1] {
		if strings.Contains(l, `"event":"codex"`) {
			t.Errorf("codex event before the Claude tick finished: %q", out)
		}
	}
	// The exit status is the Claude tick's outcome, not the Codex one.
	if code == 0 {
		t.Errorf("exit = 0; an empty Claude roster cannot have switched")
	}
}

func TestAutoOnceHumanCodexLineIsTimestamped(t *testing.T) {
	codexHome(t, nil, func(context.Context, string, string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 10.0}, "plan": "pro"}}
	})
	writeLiveAuth(t, seedOne(t))
	_, out, _ := runCodex(t, "", "auto", "--once", "--dry-run")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "  codex: ") || len(last) < 10 || last[2] != ':' || last[5] != ':' {
		t.Errorf("last line = %q, want 'HH:MM:SS  codex: ...'", last)
	}
}

func TestAutoOnceQuietCodexTickPrintsNothing(t *testing.T) {
	codexHome(t, nil, func(context.Context, string, string) api.UsageFetch {
		return api.UsageFetch{Usage: map[string]any{"five_hour": map[string]any{"pct": 10.0}, "plan": "pro"}}
	})
	writeLiveAuth(t, seedOne(t))
	_, out, _ := runCodex(t, "", "auto", "--once", "--json")
	if strings.Contains(out, `"event":"codex"`) {
		t.Errorf("an ok tick outside --dry-run was printed: %q", out)
	}
}

func TestAutoCodexDisabledOrAbsentAddsNothing(t *testing.T) {
	codexHome(t, nil, nil)
	_, out, _ := runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	if strings.Contains(out, `"event":"codex"`) {
		t.Errorf("no Codex accounts, yet a codex event: %q", out)
	}
	writeLiveAuth(t, seedOne(t))
	if _, err := settings.SetSetting(paths.GetBackupRoot(), "autoswitch.codexEnabled", "false"); err != nil {
		t.Fatal(err)
	}
	_, out, _ = runCodex(t, "", "auto", "--once", "--json", "--dry-run")
	if strings.Contains(out, `"event":"codex"`) {
		t.Errorf("codexEnabled false, yet a codex event: %q", out)
	}
}

func TestStartCodexLoopTicksImmediatelyThenStops(t *testing.T) {
	ticks := make(chan struct{}, 10)
	stop := startCodexLoop(true, time.Hour, func(context.Context) { ticks <- struct{}{} })
	select {
	case <-ticks:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not tick immediately")
	}
	stop()
	if s := startCodexLoop(false, time.Hour, func(context.Context) { t.Error("disabled loop ticked") }); s == nil {
		t.Error("nil stop")
	}
}

// TestCodexLoginCapturesTheOutgoingLoginFirst: codex login overwrites
// auth.json, so the outgoing managed account's newest (rotated) tokens are
// written into its snapshot before codex runs.
func TestCodexLoginCapturesTheOutgoingLoginFirst(t *testing.T) {
	codexHome(t, nil, nil)
	seedOne(t)
	rotated := makeCodexAuth(t, testAcctA, testUserA, "a@example.com", time.Now().Unix()+7200)
	rotated["tokens"].(map[string]any)["refresh_token"] = "rt-rotated-by-codex"
	writeLiveAuth(t, rotated)
	var snapAtSpawn map[string]any
	stubLogin(t, "/usr/local/bin/codex", nil, 0, func(string, []string) {
		snapAtSpawn = testStore().ReadSnapshot(authfile.AccountKey(testUserA, testAcctA))
		writeLiveAuth(t, makeCodexAuth(t, testAcctB, testUserB, "b@example.com", time.Now().Unix()+3600))
	})
	if code, out, errb := runCodex(t, "", "codex", "login"); code != 0 {
		t.Fatalf("exit %d out %q err %q", code, out, errb)
	}
	tok, _ := snapAtSpawn["tokens"].(map[string]any)
	if tok["refresh_token"] != "rt-rotated-by-codex" {
		t.Fatalf("snapshot at spawn holds %v, want the live login's rotated refresh token", tok["refresh_token"])
	}
}

// TestCodexListJSONKeepsControlCharactersTextStripsThem: an alias or
// workspace name holding a terminal control sequence reaches `list --json`
// as stored and is printed by the text list without it.
func TestCodexListJSONKeepsControlCharactersTextStripsThem(t *testing.T) {
	codexHome(t, nil, offlineUsage(t))
	seedOne(t)
	const alias, workspace = "w\x1b[1m", "Evil\x1b[2J"
	st := testStore()
	key := authfile.AccountKey(testUserA, testAcctA)
	if err := st.SetAlias(key, alias); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkspaceName(key, workspace); err != nil {
		t.Fatal(err)
	}

	_, out, _ := runCodex(t, "", "codex", "list", "--json", "--skip-api")
	row := decodeJSON(t, out)["accounts"].([]any)[0].(map[string]any)
	if row["alias"] != alias || row["workspace"] != workspace {
		t.Errorf("JSON row changed the stored values: alias %q workspace %q", row["alias"], row["workspace"])
	}

	_, out, _ = runCodex(t, "", "codex", "list", "--skip-api")
	if strings.Contains(out, "\x1b") {
		t.Errorf("text list carries an escape: %q", out)
	}
	if !strings.Contains(out, "a@example.com [Evil[2J] (w[1m)") {
		t.Errorf("text list = %q, want the fields as plain text", out)
	}
}
