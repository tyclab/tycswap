package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/logging"
)

// TestRefreshNeverFollowsARedirect: a 307 from the token endpoint must not
// re-POST the refresh token to the Location.
func TestRefreshNeverFollowsARedirect(t *testing.T) {
	elsewhere := newTokenServer(t, 200, `{"access_token": "stolen", "expires_in": 3600}`)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.srv.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	c := NewHTTPClient()
	c.TokenURL = redirector.URL
	if out := c.Refresh(context.Background(), oauthCreds); out.Error != ErrTransient || out.Credentials != "" {
		t.Fatalf("Refresh = %+v, want transient", out)
	}
	if elsewhere.hits != 0 {
		t.Fatalf("the refresh token was sent to the redirect target (%d hits)", elsewhere.hits)
	}
	// The zero-value client is just as strict.
	c2 := &HTTPClient{TokenURL: redirector.URL}
	c2.Refresh(context.Background(), oauthCreds)
	if elsewhere.hits != 0 {
		t.Fatal("zero-value client followed the redirect")
	}
}

func TestRefreshRejectsMalformedSuccessBodies(t *testing.T) {
	for name, body := range map[string]string{
		"numeric access_token": `{"access_token": 12345, "expires_in": 3600}`,
		"object access_token":  `{"access_token": {"a": 1}, "expires_in": 3600}`,
		"empty access_token":   `{"access_token": "", "expires_in": 3600}`,
		"huge expires_in":      `{"access_token": "t", "expires_in": 1e30}`,
		"zero expires_in":      `{"access_token": "t", "expires_in": 0}`,
		"negative expires_in":  `{"access_token": "t", "expires_in": -5}`,
		"string expires_in":    `{"access_token": "t", "expires_in": "3600"}`,
		"over 1 MiB":           `{"access_token": "t", "expires_in": 3600, "pad": "` + strings.Repeat("x", 1<<20) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			ts := newTokenServer(t, 200, body)
			c := clientFor(ts.srv.URL, "", "")
			if out := c.Refresh(context.Background(), oauthCreds); out.Error != ErrTransient || out.Credentials != "" {
				t.Fatalf("Refresh = error %q creds %d bytes, want transient", out.Error, len(out.Credentials))
			}
		})
	}
	ts := newTokenServer(t, 200, `{"access_token": "t", "expires_in": 9999999}`)
	if out := clientFor(ts.srv.URL, "", "").Refresh(context.Background(), oauthCreds); out.Credentials == "" {
		t.Fatalf("a valid response was refused: %+v", out)
	}
}

// TestRefreshErrorBodyIsNotLogged: only the OAuth error code reaches the log.
func TestRefreshErrorBodyIsNotLogged(t *testing.T) {
	dir := t.TempDir()
	prev := Log
	Log = logging.New(dir, true)
	t.Cleanup(func() { Log = prev })
	ts := newTokenServer(t, 400, `{"error": "invalid_grant", "error_description": "token rt-SECRET-ECHO is revoked"}`)
	if out := clientFor(ts.srv.URL, "", "").Refresh(context.Background(), oauthCreds); out.Error != ErrInvalidGrant {
		t.Fatalf("error = %q", out.Error)
	}
	ts2 := newTokenServer(t, 500, `<html>rt-SECRET-ECHO</html>`)
	clientFor(ts2.srv.URL, "", "").Refresh(context.Background(), oauthCreds)
	b, _ := os.ReadFile(filepath.Join(dir, "tycswap.log"))
	log := string(b)
	if strings.Contains(log, "SECRET-ECHO") {
		t.Fatalf("error body logged: %s", log)
	}
	if !strings.Contains(log, "error: invalid_grant") || !strings.Contains(log, "error: unknown") {
		t.Fatalf("error codes not logged: %s", log)
	}
}

func TestUsageAndProfileRefuseRedirects(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect followed to %s", r.URL)
	}))
	t.Cleanup(elsewhere.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	c := NewHTTPClient()
	c.UsageURL, c.ProfileURL = redirector.URL, redirector.URL
	if _, err := c.Usage(context.Background(), "at"); err == nil {
		t.Error("Usage accepted a redirect")
	}
	if id := c.Profile(context.Background(), "at"); id != nil {
		t.Error("Profile accepted a redirect")
	}
}
