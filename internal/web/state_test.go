// Tests for the GET /api/state document: exact shape, jsonout parity with
// `tycswap list --json`, sentinel statuses, and the no-credential-material
// guarantee checked against the raw bytes.
package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/version"
)

// canon round-trips v through JSON so numbers become float64 and maps compare
// structurally.
func canon(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// stripResetStrings removes the countdown/clock strings jsonout recomputes from
// the wall clock (installed by reporting's init) after asserting they are
// strings, so the rest of the document can be compared exactly.
func stripResetStrings(t *testing.T, win any) {
	t.Helper()
	m, ok := win.(map[string]any)
	if !ok {
		return
	}
	if _, has := m["resetsAt"]; has {
		if _, ok := m["countdown"].(string); !ok {
			t.Errorf("window %v lacks string countdown", m)
		}
		if _, ok := m["clock"].(string); !ok {
			t.Errorf("window %v lacks string clock", m)
		}
	}
	delete(m, "countdown")
	delete(m, "clock")
}

// asSlice is v as a JSON array, or nil.
func asSlice(v any) []any {
	a, _ := v.([]any)
	return a
}

// TestState_ExactShape pins the whole document, on a Claude-only install
// (auto.codex null, no Codex rows: the shape from before Codex) and with
// Codex accounts (their rows after the Claude ones, the Codex engine under
// auto.codex; DESIGN A47).
func TestState_ExactShape(t *testing.T) {
	t.Run("claude-only", func(t *testing.T) { testStateExactShape(t, false) })
	t.Run("with-codex", func(t *testing.T) { testStateExactShape(t, true) })
}

func testStateExactShape(t *testing.T, codex bool) {
	var opts []option
	if codex {
		opts = append(opts, withCodex())
	}
	h := newHarness(t, opts...)
	resp := h.get("/api/state")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type %q", ct)
	}
	raw := readBody(t, resp)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("state is not JSON: %v\n%s", err, raw)
	}

	accounts := got["accounts"].([]any)
	wantRows := 3
	if codex {
		wantRows = 5
	}
	if len(accounts) != wantRows {
		t.Fatalf("accounts len %d, want %d", len(accounts), wantRows)
	}
	for _, a := range accounts {
		u, _ := a.(map[string]any)["usage"].(map[string]any)
		stripResetStrings(t, u["fiveHour"])
		stripResetStrings(t, u["sevenDay"])
		for _, w := range asSlice(u["scoped"]) {
			stripResetStrings(t, w)
		}
	}

	want := map[string]any{
		"schemaVersion": 1,
		"serverTime":    "2026-09-19T10:00:00Z",
		"version":       version.Display(),
		"activeNumber":  1,
		"accounts": []any{
			map[string]any{
				"number": 1, "email": "alice@example.com", "alias": "work", "orgName": "Acme",
				"kind": "oauth", "isActive": true, "disabled": false, "switchable": true, "rotationEligible": true,
				"usageStatus": "ok", "provider": "claude", "key": "claude:1",
				"usage": map[string]any{
					"fiveHour": map[string]any{"pct": 42, "resetsAt": "2026-09-19T12:00:00Z"},
					"sevenDay": map[string]any{"pct": 100, "resetsAt": "2026-09-22T00:00:00Z"},
					"scoped":   []any{map[string]any{"name": "opus", "pct": 12.5, "resetsAt": "2026-09-22T00:00:00Z"}},
				},
				"atLimit": true, "limitingWindows": []any{"seven_day"},
				"usageFetchedAt": "2025-09-19T10:00:00Z", "usageAgeSeconds": 10.3,
			},
			map[string]any{
				"number": 2, "email": "bob@example.com", "alias": "", "orgName": "",
				"kind": "api_key", "isActive": false, "disabled": true, "switchable": true, "rotationEligible": false,
				"usageStatus": "api_key", "usage": nil, "provider": "claude", "key": "claude:2",
			},
			map[string]any{
				"number": 3, "email": "carol@example.com", "alias": "", "orgName": "",
				"kind": "oauth", "isActive": false, "disabled": false, "switchable": false, "rotationEligible": false,
				"usageStatus": "unavailable", "usage": nil, "provider": "claude", "key": "claude:3",
			},
		},
		"sessions": map[string]any{
			"claude": []any{
				map[string]any{"pid": 4242, "sessionId": "sess-1", "cwd": "/work/repo", "startedAt": 1758276000000, "kind": "interactive", "entrypoint": "cli", "status": "busy", "title": "Fix the flaky test", "profile": ""},
				map[string]any{"pid": 4343, "sessionId": "sess-2", "cwd": "/work/other", "startedAt": 1758276100000, "kind": "bg", "entrypoint": "claude-vscode", "status": nil, "title": "", "profile": ""},
			},
			"ide": []any{
				map[string]any{"port": 51234, "pid": 5555, "ideName": "Visual Studio Code", "workspaceFolders": []any{"/work/repo"}},
			},
		},
		"settings": []any{
			map[string]any{"key": "autoswitch.sevenDayThreshold", "kind": "float", "value": 97, "default": 97, "isDefault": true, "description": "Switch when the 7d window reaches this pct", "min": 50, "max": 100, "applies": settingApplies("autoswitch.sevenDayThreshold")},
			map[string]any{"key": "autoswitch.codexThreshold", "kind": "float", "value": 0, "default": 0, "isDefault": true, "description": "Codex-only switch threshold (0 = use autoswitch.sevenDayThreshold)", "min": 0, "max": 99.9, "applies": settingApplies("autoswitch.codexThreshold")},
			map[string]any{"key": "autoswitch.codexEnabled", "kind": "bool", "value": true, "default": true, "isDefault": true, "description": "Also auto-switch Codex accounts", "applies": settingApplies("autoswitch.codexEnabled")},
			map[string]any{"key": "autoswitch.strategy", "kind": "choice", "value": "best", "default": "soonest-reset", "isDefault": false, "choices": []any{"best", "soonest-reset"}, "description": "How auto-switch orders qualifying targets", "applies": settingApplies("autoswitch.strategy")},
		},
		"auto": map[string]any{
			"available": true, "running": true, "dryRun": true, "startedAt": 1758276000, "threshold": 85,
			"settings": map[string]any{"autoswitch.sevenDayThreshold": 85, "autoswitch.strategy": "soonest-reset", "autoswitch.codexEnabled": true},
			"events": []any{
				map[string]any{"at": 1758276001, "kind": "poll", "message": "polled 3 accounts"},
				map[string]any{"at": 1758276002, "kind": "switch", "message": "switched to #2", "account": "2", "fields": map[string]any{"from": "1"}},
			},
			"quarantine": map[string]any{"quarantine": map[string]any{"3": map[string]any{"reason": "invalid_grant", "at": 1758275000}}},
			"codex":      nil,
		},
		"strategies":    []any{"best", "next-available"},
		"name":          "tycswap",
		"currentLogin":  map[string]any{"email": "alice@example.com", "saved": true},
		"authOverrides": map[string]any{"env": []any{}, "settings": []any{}, "settingsPath": "/home/t/.claude/settings.json"},
		"updates":       map[string]any{"available": false, "checking": false, "app": map[string]any{"current": "v0.4.0", "available": false}, "claudeCode": map[string]any{"state": "checking", "available": false}},
		"ui":            map[string]any{"folded": map[string]any{}},
	}
	if codex {
		// The Codex rows: the Claude row shape, with only the 5h and 7d
		// windows, never atLimit, tokenStatus or baseUrl.
		want["accounts"] = append(want["accounts"].([]any),
			map[string]any{
				"number": 1, "email": "dana@example.com", "alias": "", "orgName": "Team",
				"kind": "oauth", "isActive": true, "disabled": false, "switchable": true, "rotationEligible": true,
				"usageStatus": "ok", "provider": "codex", "key": "codex:1",
				"usage": map[string]any{
					"fiveHour": map[string]any{"pct": 30, "resetsAt": "2026-09-19T13:00:00Z"},
					"sevenDay": map[string]any{"pct": 55, "resetsAt": "2026-09-24T00:00:00Z"},
				},
				"usageFetchedAt": "2025-09-19T10:00:00Z", "usageAgeSeconds": 4,
			},
			map[string]any{
				"number": 2, "email": "erin@example.com", "alias": "ci", "orgName": "",
				"kind": "api_key", "isActive": false, "disabled": true, "switchable": false, "rotationEligible": false,
				"usageStatus": "api_key", "usage": nil, "provider": "codex", "key": "codex:2",
			},
		)
		want["auto"].(map[string]any)["codex"] = map[string]any{
			"enabled": true, "running": true, "threshold": 97,
			"lastTick": map[string]any{"at": 1758276003, "outcome": "switched", "detail": "switched 1 (98%) -> 2 (12%)", "switchedTo": "2", "runningPids": []any{777}},
		}
	}
	if _, has := got["tokenStatus"]; has {
		t.Error("tokenStatus present at top level")
	}
	for _, a := range accounts {
		if _, has := a.(map[string]any)["tokenStatus"]; has {
			t.Error("tokenStatus present without ?tokenStatus=1")
		}
	}
	if seq, ok := got["sequence"].(float64); !ok || seq <= 0 {
		t.Fatalf("missing state sequence: %v", got["sequence"])
	}
	delete(got, "sequence")
	if !reflect.DeepEqual(canon(t, got), canon(t, want)) {
		gb, _ := json.MarshalIndent(canon(t, got), "", "  ")
		wb, _ := json.MarshalIndent(canon(t, want), "", "  ")
		t.Fatalf("state mismatch\n got: %s\nwant: %s", gb, wb)
	}
}

func TestState_NoCredentialMaterial(t *testing.T) {
	h := newHarness(t)
	raw := readBody(t, h.get("/api/state"))
	for _, needle := range []string{h.s.Token(), h.s.cookie, h.s.launch, "lastKey", "accessToken", "refreshToken", h.s.cookieName()} {
		if bytes.Contains(raw, []byte(needle)) {
			t.Errorf("state body contains %q", needle)
		}
	}
	// The SSE stream carries the same document.
	st := h.openSSE()
	defer st.close()
	ev := st.nextState(t, timeout)
	for _, needle := range []string{h.s.Token(), h.s.cookie, "lastKey"} {
		if strings.Contains(ev.data, needle) {
			t.Errorf("SSE state contains %q", needle)
		}
	}
}

func TestState_PassesNilFetchSet(t *testing.T) {
	h := newHarness(t)
	readBody(t, h.get("/api/state"))
	h.fa.mu.Lock()
	defer h.fa.mu.Unlock()
	if len(h.fa.fetchArgs) == 0 {
		t.Fatal("AccountsSnapshot not called")
	}
	if h.fa.fetchArgs[len(h.fa.fetchArgs)-1] != nil {
		t.Fatalf("fetch set = %v, want nil (every stale account eligible, store-paced)", h.fa.fetchArgs)
	}
}

func TestState_TokenStatusEnrichment(t *testing.T) {
	h := newHarness(t)
	var got map[string]any
	if err := json.Unmarshal(readBody(t, h.get("/api/state?tokenStatus=1")), &got); err != nil {
		t.Fatal(err)
	}
	rows := got["accounts"].([]any)
	wantTS := map[float64]string{1: "oauth: fresh, refresh token yes, expires 12:00 in 2h", 2: "", 3: ""}
	for _, r := range rows {
		row := r.(map[string]any)
		ts, has := row["tokenStatus"]
		if !has {
			t.Errorf("row %v lacks tokenStatus", row["number"])
			continue
		}
		if ts != wantTS[row["number"].(float64)] {
			t.Errorf("row %v tokenStatus %q, want %q", row["number"], ts, wantTS[row["number"].(float64)])
		}
	}
	// ListAccounts is asked for the token-status JSON payload, store-only.
	if calls := h.ops.Calls(); !reflect.DeepEqual(calls, []string{"ListAccounts(true,true,len=0)"}) {
		t.Fatalf("ops calls %v", calls)
	}
	// Truthy spellings all work; falsy ones and absence do not enrich.
	for _, q := range []string{"?tokenStatus=true", "?tokenStatus=yes", "?tokenStatus=on"} {
		_ = json.Unmarshal(readBody(t, h.get("/api/state"+q)), &got)
		if _, has := got["accounts"].([]any)[0].(map[string]any)["tokenStatus"]; !has {
			t.Errorf("%s: no enrichment", q)
		}
	}
	for _, q := range []string{"", "?tokenStatus=0", "?tokenStatus=false", "?tokenStatus="} {
		_ = json.Unmarshal(readBody(t, h.get("/api/state"+q)), &got)
		if _, has := got["accounts"].([]any)[0].(map[string]any)["tokenStatus"]; has {
			t.Errorf("%q: enriched without the flag", q)
		}
	}
	// SSE states never carry it (no per-request flag on the stream).
	st := h.openSSE()
	defer st.close()
	_ = json.Unmarshal([]byte(st.nextState(t, timeout).data), &got)
	if _, has := got["accounts"].([]any)[0].(map[string]any)["tokenStatus"]; has {
		t.Error("SSE state carries tokenStatus")
	}
}

func TestState_TokenStatus_OddPayloadAndListError(t *testing.T) {
	h := newHarness(t)
	h.ops.mu.Lock()
	h.ops.listPayload = map[string]any{"accounts": []any{map[string]any{"number": 2, "tokenStatus": "oauth: expired"}, "junk", map[string]any{"number": "3", "tokenStatus": 5}}}
	h.ops.mu.Unlock()
	var got map[string]any
	_ = json.Unmarshal(readBody(t, h.get("/api/state?tokenStatus=1")), &got)
	rows := got["accounts"].([]any)
	if rows[1].(map[string]any)["tokenStatus"] != "oauth: expired" {
		t.Errorf("status not lifted by number: %v", rows[1])
	}
	if rows[2].(map[string]any)["tokenStatus"] != "" {
		t.Errorf("non-string status not blanked: %v", rows[2])
	}

	h.ops.mu.Lock()
	h.ops.listErr = cerr.Config("cannot read store")
	h.ops.mu.Unlock()
	resp := h.get("/api/state?tokenStatus=1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list error must not fail the state: %d", resp.StatusCode)
	}
	_ = json.Unmarshal(readBody(t, resp), &got)
	for _, r := range got["accounts"].([]any) {
		if ts, has := r.(map[string]any)["tokenStatus"]; !has || ts != "" {
			t.Errorf("row after list error: %v", r)
		}
	}
	found := false
	for _, l := range h.Logs() {
		if l == "web: token status: cannot read store" {
			found = true
		}
	}
	if !found {
		t.Fatalf("list error not logged: %v", h.Logs())
	}
}

func TestState_TokenStatus_NilAccountOps(t *testing.T) {
	h := newHarness(t, withNoAccounts())
	var got map[string]any
	_ = json.Unmarshal(readBody(t, h.get("/api/state?tokenStatus=1")), &got)
	for _, r := range got["accounts"].([]any) {
		if _, has := r.(map[string]any)["tokenStatus"]; has {
			t.Errorf("tokenStatus present without AccountOps: %v", r)
		}
	}
}

func TestState_NilSectionsAndEmptyLists(t *testing.T) {
	h := newHarness(t, withNoSettings(), withNoAuto(), withNoUpdates(), withNoUIPrefs(), withNoCurrentLogin())
	h.fa.mu.Lock()
	h.fa.snap = &reporting.AccountsSnapshot{}
	h.fa.mu.Unlock()
	h.setSessions(SessionsView{})
	var got map[string]any
	if err := json.Unmarshal(readBody(t, h.get("/api/state")), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"settings", "auto", "updates", "ui", "currentLogin"} {
		if got[k] != nil {
			t.Errorf("nil facade section %q must serialise as null: %v", k, got[k])
		}
	}
	// Exactly the manual strategies the CLI accepts; soonest-reset is
	// auto-switch only and must never be offered here.
	if s, ok := got["strategies"].([]any); !ok || len(s) != 2 || s[0] != "best" || s[1] != "next-available" {
		t.Errorf("strategies = %v, want [best next-available]", got["strategies"])
	}
	if got["activeNumber"] != nil {
		t.Errorf("activeNumber = %v, want null", got["activeNumber"])
	}
	if a, ok := got["accounts"].([]any); !ok || len(a) != 0 {
		t.Errorf("accounts = %v, want []", got["accounts"])
	}
	sess := got["sessions"].(map[string]any)
	if c, ok := sess["claude"].([]any); !ok || len(c) != 0 {
		t.Errorf("sessions.claude = %v, want []", sess["claude"])
	}
	if i, ok := sess["ide"].([]any); !ok || len(i) != 0 {
		t.Errorf("sessions.ide = %v, want []", sess["ide"])
	}
}

func TestState_NilSnapshotTolerated(t *testing.T) {
	h := newHarness(t)
	h.fa.mu.Lock()
	h.fa.snap = nil
	h.fa.mu.Unlock()
	resp := h.get("/api/state")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.Unmarshal(readBody(t, resp), &got); err != nil {
		t.Fatal(err)
	}
	if a, ok := got["accounts"].([]any); !ok || len(a) != 0 {
		t.Errorf("accounts = %v", got["accounts"])
	}
}

func TestAccountRow_UsageStatusSentinels(t *testing.T) {
	cases := []struct {
		entry      usage.UsageEntry
		wantStatus string
		wantUsage  bool
	}{
		{usage.UsageEntry{Sentinel: jsonout.UsageTokenExpired}, "token_expired", false},
		{usage.UsageEntry{Sentinel: jsonout.UsageAPIKey}, "api_key", false},
		{usage.UsageEntry{Sentinel: jsonout.UsageKeychainUnavailable}, "keychain_unavailable", false},
		{usage.UsageEntry{Sentinel: jsonout.UsageReloginRequired}, "relogin_required", false},
		{usage.UsageEntry{Sentinel: jsonout.UsageNoCredentials}, "no_credentials", false},
		{usage.UsageEntry{Sentinel: "something else"}, "no_credentials", false},
		{usage.UsageEntry{}, "unavailable", false},
		// Stale beyond STALE_OK_S and not trust-extended → decision value nil.
		{usage.UsageEntry{LastGood: map[string]any{"five_hour": map[string]any{"pct": 1}}, FetchedAt: f64(1), AgeS: f64(usage.StaleOKS + 1)}, "unavailable", false},
		// Stale but trust-extended → still served.
		{usage.UsageEntry{LastGood: map[string]any{"five_hour": map[string]any{"pct": 1}}, FetchedAt: f64(1), AgeS: f64(usage.StaleOKS + 1), TrustExtended: true}, "ok", true},
		{usage.UsageEntry{LastGood: map[string]any{"five_hour": map[string]any{"pct": 1}}, FetchedAt: f64(1), AgeS: f64(5)}, "ok", true},
	}
	for i, tc := range cases {
		row := accountRow(reporting.AccountSnapshot{Number: "7", Usage: tc.entry})
		if row["usageStatus"] != tc.wantStatus {
			t.Errorf("case %d: usageStatus %v, want %s", i, row["usageStatus"], tc.wantStatus)
		}
		if (row["usage"] != nil) != tc.wantUsage {
			t.Errorf("case %d: usage present=%v, want %v", i, row["usage"] != nil, tc.wantUsage)
		}
		if tc.wantUsage {
			if _, ok := row["usageFetchedAt"]; !ok {
				t.Errorf("case %d: usageFetchedAt missing alongside usage", i)
			}
		} else if _, ok := row["usageFetchedAt"]; ok {
			t.Errorf("case %d: usageFetchedAt present without usage", i)
		}
		if _, ok := row["atLimit"]; ok {
			t.Errorf("case %d: atLimit present when false", i)
		}
	}
}

func TestAccountRow_ParityWithListJSON(t *testing.T) {
	// The usage / at-limit / freshness keys must be byte-identical to what
	// jsonout.AccountRow (tycswap list --json) produces for the same inputs.
	for _, a := range sampleSnapshot().Accounts {
		row := accountRow(a)
		ref := jsonout.AccountRow(0, a.Email, a.OrgName, a.OrgUUID, a.IsActive, a.Usage.DecisionValue(), jsonout.RowOpts{
			UsageFetchedAt: a.Usage.FetchedAt, UsageAgeS: a.Usage.AgeS, Alias: a.Alias, Disabled: a.Disabled,
			AtLimit: a.AtLimit, LimitingWindows: a.LimitingWindows,
		})
		for _, k := range []string{"usageStatus", "usage", "atLimit", "limitingWindows", "usageFetchedAt", "usageAgeSeconds"} {
			gv, gok := row[k]
			rv, rok := ref[k]
			if gok != rok {
				t.Errorf("account %s key %q: present=%v, list --json present=%v", a.Number, k, gok, rok)
				continue
			}
			if gok && !reflect.DeepEqual(canon(t, gv), canon(t, rv)) {
				t.Errorf("account %s key %q: %v != list --json %v", a.Number, k, gv, rv)
			}
		}
	}
}

// A Codex row is projected by the same accountRow: its usage is what
// `tycswap codex list --json` serialises for the account (jsonout.UsageToJSON
// over the measurement), and the usage / freshness keys are those of a list
// row; it never carries the at-limit keys, which are Claude's.
func TestAccountRow_ParityWithListJSON_Codex(t *testing.T) {
	for _, a := range sampleCodexRows() {
		row := accountRow(a)
		var codexList any
		if a.Usage.LastGood != nil {
			codexList = jsonout.UsageToJSON(a.Usage.LastGood)
		}
		if !reflect.DeepEqual(canon(t, row["usage"]), canon(t, codexList)) {
			t.Errorf("codex %s usage %v != codex list --json %v", a.Number, row["usage"], codexList)
		}
		ref := jsonout.AccountRow(0, a.Email, a.OrgName, a.OrgUUID, a.IsActive, a.Usage.DecisionValue(), jsonout.RowOpts{
			UsageFetchedAt: a.Usage.FetchedAt, UsageAgeS: a.Usage.AgeS, Alias: a.Alias, Disabled: a.Disabled,
		})
		for _, k := range []string{"usageStatus", "usage", "usageFetchedAt", "usageAgeSeconds"} {
			gv, gok := row[k]
			rv, rok := ref[k]
			if gok != rok || (gok && !reflect.DeepEqual(canon(t, gv), canon(t, rv))) {
				t.Errorf("codex %s key %q: %v (present %v), list row %v (present %v)", a.Number, k, gv, gok, rv, rok)
			}
		}
		for _, k := range []string{"atLimit", "limitingWindows", "tokenStatus", "baseUrl"} {
			if _, has := row[k]; has {
				t.Errorf("codex %s row carries %q", a.Number, k)
			}
		}
		if row["provider"] != "codex" || row["key"] != "codex:"+a.Number {
			t.Errorf("codex %s: provider %v key %v", a.Number, row["provider"], row["key"])
		}
	}
}

// Token status is a Claude row's: with Codex rows listed, ?tokenStatus=1
// enriches the Claude rows and leaves the Codex rows without the key.
func TestState_TokenStatus_ClaudeRowsOnly(t *testing.T) {
	h := newHarness(t, withCodex())
	var got map[string]any
	if err := json.Unmarshal(readBody(t, h.get("/api/state?tokenStatus=1")), &got); err != nil {
		t.Fatal(err)
	}
	for _, r := range got["accounts"].([]any) {
		row := r.(map[string]any)
		_, has := row["tokenStatus"]
		if want := row["provider"] == "claude"; has != want {
			t.Errorf("row %v: tokenStatus present %v, want %v", row["key"], has, want)
		}
	}
}

// The endpoint profile is the login while the active CLAUDE account has the
// base URL: an active Codex row, listed first here, says nothing about it.
func TestState_ProfileFollowsTheClaudeActiveAccountOnly(t *testing.T) {
	h := newHarness(t)
	snap := sampleSnapshot()
	snap.Accounts[0].Kind, snap.Accounts[0].BaseURL = "api_key", "https://gw.example.com"
	codex := sampleCodexRows()
	snap.Accounts = append([]reporting.AccountSnapshot{codex[0]}, snap.Accounts...)
	h.fa.mu.Lock()
	h.fa.snap = snap
	h.fa.mu.Unlock()
	h.setOverrides(AuthOverridesView{Env: []string{}, Settings: []string{},
		Profile: []string{"env.ANTHROPIC_AUTH_TOKEN", "env.ANTHROPIC_BASE_URL"}, SettingsPath: "/home/t/.claude/settings.json"})
	st := decodeJSON(t, h.get("/api/state"))
	if got := st["authOverrides"].(map[string]any)["settings"].([]any); len(got) != 0 {
		t.Errorf("settings overrides %v, want none while the Claude endpoint account is active", got)
	}
}

func TestAccountRow_NumberFallsBackToString(t *testing.T) {
	row := accountRow(reporting.AccountSnapshot{Number: "x9"})
	if row["number"] != "x9" {
		t.Fatalf("number = %v", row["number"])
	}
	row = accountRow(reporting.AccountSnapshot{Number: "12"})
	if row["number"] != 12 {
		t.Fatalf("number = %v (%T)", row["number"], row["number"])
	}
}

func TestStatusFor(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{cerr.AccountNotFound("nope"), http.StatusNotFound},
		{cerr.Validation("bad"), http.StatusBadRequest},
		{cerr.Lock("busy"), http.StatusConflict},
		{cerr.ClaudeCodeLockTimeout("cc"), http.StatusLocked},
		{cerr.Config("cfg"), http.StatusInternalServerError},
		{cerr.Switch("sw"), http.StatusInternalServerError},
		{cerr.Credential("cred"), http.StatusInternalServerError},
		{errors.New("plain"), http.StatusInternalServerError},
		{errors.Join(errors.New("outer"), cerr.Validation("inner")), http.StatusBadRequest},
		{httpErr(http.StatusNotFound, "gone"), http.StatusNotFound},
		{httpErr(http.StatusConflict, "changed"), http.StatusConflict},
	}
	for _, tc := range cases {
		if got := statusFor(tc.err); got != tc.want {
			t.Errorf("statusFor(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
