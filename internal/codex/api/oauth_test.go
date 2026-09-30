// oauth_test.go — Codex token refresh (claude-swap PR #252
// tests/test_codex_oauth.py), driven by httptest servers.

package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.dpemmons.com/dpemmons/cswap/internal/logging"
)

func TestRefresh_UpdatesTheAccessToken(t *testing.T) {
	newJWT := makeJWT(t, map[string]any{"exp": 1_900_000_000, "email": "a@example.com"})
	s := newServer(t, 200, mustJSON(t, map[string]any{
		"access_token": newJWT, "refresh_token": "rt-new", "id_token": newJWT,
	}), nil)
	out := clientFor(s).TryRefresh(context.Background(), makeAuthJSON(t, "rt-old", 0))
	if out.Kind != KindOK {
		t.Fatalf("kind = %q, want success", out.Kind)
	}
	tokens := out.Payload["tokens"].(map[string]any)
	if tokens["access_token"] != newJWT || tokens["id_token"] != newJWT {
		t.Errorf("tokens not updated: %v", tokens)
	}
	if tokens["refresh_token"] != "rt-new" {
		t.Errorf("refresh_token = %v, want rt-new", tokens["refresh_token"])
	}
	if tokens["account_id"] != "2f4dac8f-f15f-4c58-a567-e96985d51cfd" {
		t.Errorf("unrelated token fields must survive: %v", tokens)
	}
}

// RFC 6749 §6: an absent refresh_token means keep the one you have.
func TestRefresh_KeepsTheOldRefreshTokenWhenNoneIsReturned(t *testing.T) {
	for _, body := range []string{`{"access_token":"at"}`, `{"access_token":"at","refresh_token":null}`, `{"access_token":"at","refresh_token":""}`} {
		s := newServer(t, 200, body, nil)
		out := clientFor(s).TryRefresh(context.Background(), makeAuthJSON(t, "rt-old", 0))
		if got := out.Payload["tokens"].(map[string]any)["refresh_token"]; got != "rt-old" {
			t.Errorf("body %s: refresh_token = %v, want rt-old", body, got)
		}
	}
}

// The caller may still need the pre-refresh payload if the persist fails.
func TestRefresh_DoesNotMutateThePayloadItWasGiven(t *testing.T) {
	s := newServer(t, 200, `{"access_token":"at","refresh_token":"rt-new"}`, nil)
	payload := makeAuthJSON(t, "rt-old", 0)
	clientFor(s).TryRefresh(context.Background(), payload)
	tokens := payload["tokens"].(map[string]any)
	if tokens["refresh_token"] != "rt-old" || tokens["access_token"] == "at" {
		t.Errorf("input payload mutated: %v", tokens)
	}
	if payload["last_refresh"] != "2026-08-16T00:00:00Z" {
		t.Errorf("input last_refresh mutated: %v", payload["last_refresh"])
	}
}

func TestRefresh_StampsLastRefresh(t *testing.T) {
	s := newServer(t, 200, `{"access_token":"at"}`, nil)
	payload := defaultAuth(t)
	out := clientFor(s).TryRefresh(context.Background(), payload)
	got, _ := out.Payload["last_refresh"].(string)
	if got == payload["last_refresh"] {
		t.Fatalf("last_refresh not restamped: %q", got)
	}
	if _, err := time.Parse("2006-01-02T15:04:05Z", got); err != nil {
		t.Errorf("last_refresh %q not in codex's format: %v", got, err)
	}
}

func TestApplyRefresh_StampFormat(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 34, 56, 789, time.UTC)
	out := applyRefresh(map[string]any{}, map[string]any{}, map[string]any{"access_token": "at"}, now)
	if out["last_refresh"] != "2026-09-30T12:34:56Z" {
		t.Errorf("last_refresh = %v", out["last_refresh"])
	}
}

// Persisting a tokenless 200 would store a stale access token alongside a
// possibly already-spent refresh token.
func TestRefresh_A200WithoutATokenIsNotSuccess(t *testing.T) {
	for _, body := range []string{`{}`, `{"access_token":""}`, `{"access_token":null}`, `[1,2]`, `"junk"`, `not json`} {
		s := newServer(t, 200, body, nil)
		out := clientFor(s).TryRefresh(context.Background(), defaultAuth(t))
		if out.Kind != KindTransient || out.Payload != nil {
			t.Errorf("body %s: outcome = %+v, want transient", body, out)
		}
	}
}

func TestRefresh_PreRequestVerdicts(t *testing.T) {
	noRT := defaultAuth(t)
	noRT["tokens"].(map[string]any)["refresh_token"] = nil
	emptyRT := defaultAuth(t)
	emptyRT["tokens"].(map[string]any)["refresh_token"] = ""
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"missing refresh token", noRT, KindNoRefreshToken},
		{"empty refresh token", emptyRT, KindNoRefreshToken},
		{"api key account", map[string]any{"auth_mode": "apikey", "tokens": nil}, KindNotApplicable},
		{"api key present", map[string]any{"OPENAI_API_KEY": "sk-x"}, KindNotApplicable},
		{"no tokens, no key", map[string]any{"auth_mode": "chatgpt"}, KindTransient},
		{"nil payload", nil, KindTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, 200, `{"access_token":"at"}`, nil)
			if got := clientFor(s).TryRefresh(context.Background(), tc.payload).Kind; got != tc.want {
				t.Errorf("kind = %q, want %q", got, tc.want)
			}
			if s.hits() != 0 {
				t.Errorf("made %d requests, want none", s.hits())
			}
		})
	}
}

// The classification matrix: nested (live) and flat (RFC 6749) error shapes,
// permanent only for 400/401/403 with a listed code.
func TestRefresh_ErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"nested shape (verified live)", 401, `{"error":{"message":"Could not validate your token.","type":"invalid_request_error","code":"token_expired"}}`, KindTokenExpired},
		{"flat rfc shape", 400, `{"error":"invalid_grant"}`, KindInvalidGrant},
		{"rejected client", 401, `{"error":{"code":"invalid_client"}}`, KindInvalidClient},
		{"nested type fallback", 403, `{"error":{"type":"invalid_grant"}}`, KindInvalidGrant},
		{"unrecognised code stays transient", 400, `{"error":{"code":"rate_limited"}}`, KindTransient},
		{"unparseable body stays transient", 400, `not json at all`, KindTransient},
		{"non-object body stays transient", 400, `["invalid_grant"]`, KindTransient},
		{"server error is transient", 503, `{"error":{"code":"invalid_grant"}}`, KindTransient},
		{"404 is transient", 404, `{"error":"invalid_grant"}`, KindTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, tc.status, tc.body, nil)
			out := clientFor(s).TryRefresh(context.Background(), defaultAuth(t))
			if out.Kind != tc.want || out.Payload != nil {
				t.Errorf("outcome = %+v, want kind %q", out, tc.want)
			}
		})
	}
}

func TestRefresh_ANetworkFailureIsTransient(t *testing.T) {
	if got := deadClient(t).TryRefresh(context.Background(), defaultAuth(t)).Kind; got != KindTransient {
		t.Errorf("kind = %q, want transient", got)
	}
}

// Both were established against the live endpoint; a silent change to either
// turns every refresh into invalid_client.
func TestRefresh_UsesTheVerifiedClientIDAndJSONBody(t *testing.T) {
	s := newServer(t, 200, `{"access_token":"at"}`, nil)
	clientFor(s).TryRefresh(context.Background(), makeAuthJSON(t, "rt-x", 0))
	req := s.last(t)
	if req.Method != "POST" || req.Path != "/oauth/token" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if ua := req.Header.Get("User-Agent"); ua != UserAgent {
		t.Errorf("User-Agent = %q", ua)
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	want := map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": "rt-x",
		"client_id":     "app_EMoamEEZ73f0CkXaXp7hrann",
		"scope":         "openid profile email offline_access",
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v, want %v", body, want)
	}
	if NewHTTPClient().TokenURL != "https://auth.openai.com/oauth/token" {
		t.Errorf("production TokenURL = %q", NewHTTPClient().TokenURL)
	}
}

func TestNeedsRefresh(t *testing.T) {
	now := float64(time.Now().Unix())
	cases := []struct {
		name    string
		payload any
		want    bool
	}{
		{"expired", makeAuthJSON(t, "rt", 0), true},
		{"well inside the window", makeAuthJSON(t, "rt", now+3600), false},
		{"inside the margin", makeAuthJSON(t, "rt", now+30), true},
		{"exactly at the margin", makeAuthJSON(t, "rt", now+ExpiryMarginS), true},
		{"unreadable expiry", map[string]any{"tokens": map[string]any{"access_token": "x"}}, true},
		{"not a payload", "nope", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsRefresh(tc.payload, now); got != tc.want {
				t.Errorf("NeedsRefresh = %v, want %v", got, tc.want)
			}
		})
	}
}

// A refresh token in a log line is a credential leak with a long tail — these
// logs are what users paste into public issues.
func TestRefresh_NoTokenValueEverReachesALog(t *testing.T) {
	dir := t.TempDir()
	Log = logging.New(dir, true)
	t.Cleanup(func() { Log = nil })

	s := newServer(t, 401, `{"error":{"code":"token_expired","message":"SECRET-RT echoed"}}`, nil)
	clientFor(s).TryRefresh(context.Background(), makeAuthJSON(t, "SECRET-RT", 0))
	deadClient(t).TryRefresh(context.Background(), makeAuthJSON(t, "SECRET-RT", 0))

	data, err := os.ReadFile(filepath.Join(dir, "claude-swap.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(data)
	if strings.Contains(log, "SECRET-RT") {
		t.Errorf("log leaked the refresh token: %s", log)
	}
	if !strings.Contains(log, "http-401 (token_expired)") {
		t.Errorf("log missing status and code: %s", log)
	}
}
