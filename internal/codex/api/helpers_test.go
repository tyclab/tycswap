// helpers_test.go — shared fixtures for the internal/codex/api tests: an
// unsigned-JWT builder, the auth.json payload builder from claude-swap PR #252
// tests/conftest_codex.py, and a recording httptest server. No test here
// touches the real network.

package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// makeJWT builds an unsigned JWT carrying claims. Nothing in cswap verifies
// these tokens — the server does — so an unsigned token is a faithful stand-in.
func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	seg := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal claims: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return seg(map[string]any{"alg": "none", "typ": "JWT"}) + "." + seg(claims) + ".sig"
}

// makeAuthJSON builds an auth.json payload in the real shape codex writes,
// with the access token expiring at exp (POSIX seconds).
func makeAuthJSON(t *testing.T, refreshToken string, exp float64) map[string]any {
	t.Helper()
	token := makeJWT(t, map[string]any{
		"exp":   int64(exp),
		"email": "a@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "2f4dac8f-f15f-4c58-a567-e96985d51cfd",
			"chatgpt_user_id":    "user-K6XCCWw4gcRpfaGR6VQKAFgA",
			"chatgpt_plan_type":  "pro",
		},
	})
	return map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      token,
			"access_token":  token,
			"refresh_token": refreshToken,
			"account_id":    "2f4dac8f-f15f-4c58-a567-e96985d51cfd",
		},
		"last_refresh": "2026-08-16T00:00:00Z",
	}
}

// defaultAuth is makeAuthJSON with the conftest defaults (rt-a, exp +1h).
func defaultAuth(t *testing.T) map[string]any {
	return makeAuthJSON(t, "rt-a", float64(time.Now().Unix()+3600))
}

// recorded is one request as the server saw it.
type recorded struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// server replies with a fixed status, headers and body, recording requests.
type server struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []recorded
	status int
	header map[string]string
	body   string
}

func newServer(t *testing.T, status int, body string, header map[string]string) *server {
	t.Helper()
	s := &server{status: status, body: body, header: header}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: b})
		s.mu.Unlock()
		for k, v := range s.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *server) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *server) last(t *testing.T) recorded {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		t.Fatal("server saw no request")
	}
	return s.reqs[len(s.reqs)-1]
}

// clientFor points every URL of an HTTPClient at s.
func clientFor(s *server) *HTTPClient {
	c := NewHTTPClient()
	c.TokenURL = s.srv.URL + "/oauth/token"
	c.UsageURL = s.srv.URL + "/backend-api/wham/usage"
	c.AccountsURL = s.srv.URL + "/backend-api/accounts"
	return c
}

// deadClient points every URL at a closed server, so each request fails at
// the transport layer.
func deadClient(t *testing.T) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := NewHTTPClient()
	c.TokenURL, c.UsageURL, c.AccountsURL = url, url, url
	return c
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
