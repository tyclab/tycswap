// Tests for the log lines of the active login's own refresh and the reply
// wait over HTTP/2 (DESIGN A129).
package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/logging"
)

func captureOAuthLog(t *testing.T) func() string {
	t.Helper()
	dir := t.TempDir()
	prev := Log
	Log = logging.New(dir, false)
	t.Cleanup(func() { Log = prev })
	return func() string {
		raw, _ := os.ReadFile(filepath.Join(dir, "tycswap.log"))
		return string(raw)
	}
}

// noTokens fails when a token of the credential reached the log.
func noTokens(t *testing.T, log string, tokens ...string) {
	t.Helper()
	for _, tok := range tokens {
		if strings.Contains(log, tok) {
			t.Errorf("the log carries the token %q:\n%s", tok, log)
		}
	}
}

// The active login's refresh while no client runs logs as the guarded ones
// do, by account number: INFO once sent, WARNING when no verdict came back or
// for invalid_grant, and never a token or the address.
func TestActiveRefreshLogsItsOutcome(t *testing.T) {
	expired := `{"claudeAiOauth": {"accessToken": "at-old-1", "refreshToken": "rt-old-1", "expiresAt": 1000}}`
	for _, tc := range []struct {
		name  string
		reply func(w http.ResponseWriter, r *http.Request, release <-chan struct{})
		want  []string
	}{
		{"no reply", func(_ http.ResponseWriter, r *http.Request, release <-chan struct{}) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}, []string{"INFO - Refreshing the token of account 1", "WARNING - The token refresh of account 1 was sent but got no answer"}},
		{"invalid_grant", func(w http.ResponseWriter, _ *http.Request, _ <-chan struct{}) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error": "invalid_grant"}`)
		}, []string{"INFO - Refreshing the token of account 1", "WARNING - The token refresh of account 1 failed (invalid_grant)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logText := captureOAuthLog(t)
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				if strings.HasSuffix(r.URL.Path, "/token") {
					tc.reply(w, r, release)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(release) })
			c := &HTTPClient{Client: &http.Client{}, TokenURL: srv.URL + "/token", UsageURL: srv.URL + "/usage",
				RefreshSendTimeout: 50 * time.Millisecond, RefreshReplyTimeout: 150 * time.Millisecond}
			TryFetchUsageForAccount(context.Background(), c, "1", "user@example.com", expired, false, func(string, string, string) error { return nil })
			log := logText()
			for _, want := range tc.want {
				if !strings.Contains(log, want) {
					t.Errorf("log lacks %q:\n%s", want, log)
				}
			}
			noTokens(t, log, "at-old-1", "rt-old-1", "user@example.com")
		})
	}
}

// A reply later than the send bound is read over HTTP/2 too, the protocol
// the token endpoint speaks.
func TestRefreshWaitsForReplyPastSendBoundOverHTTP2(t *testing.T) {
	proto := make(chan string, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		proto <- r.Proto
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{"access_token": "new-access", "refresh_token": "rt-2", "expires_in": 3600}`)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	c := &HTTPClient{Client: srv.Client(), TokenURL: srv.URL + "/token", RefreshSendTimeout: 50 * time.Millisecond, RefreshReplyTimeout: 5 * time.Second}
	out := c.Refresh(context.Background(), oauthCreds)
	if got := <-proto; got != "HTTP/2.0" {
		t.Fatalf("the request arrived as %s, want HTTP/2.0", got)
	}
	if out.Credentials == "" || out.Error != "" || out.Unanswered {
		t.Errorf("Refresh over HTTP/2 = %+v, want the rotated credential", out)
	}
}
