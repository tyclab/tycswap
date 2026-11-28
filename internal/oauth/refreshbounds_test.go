// Tests for the refresh's two bounds (DESIGN A129): until the request is
// written, then for the reply.

package oauth

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// slowTokenServer answers a valid token reply after delay, or not before the
// client gives up when delay is negative.
func slowTokenServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		wait := time.After(delay)
		if delay < 0 {
			wait = nil
		}
		select {
		case <-wait:
		case <-r.Context().Done():
			return
		case <-release:
			return
		}
		_, _ = io.WriteString(w, `{"access_token": "new-access", "refresh_token": "rt-2", "expires_in": 3600}`)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv.URL
}

func boundedClient(url string, send, reply time.Duration) *HTTPClient {
	return &HTTPClient{Client: &http.Client{}, TokenURL: url, RefreshSendTimeout: send, RefreshReplyTimeout: reply}
}

// A server that never accepts the connection holds the TLS handshake: the
// request is never written, so the send bound ends it and nothing was spent.
func TestRefreshNeverSentEndsAtSendBound(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	c := boundedClient("https://"+ln.Addr().String()+"/token", 50*time.Millisecond, 10*time.Second)
	start := time.Now()
	out := c.Refresh(context.Background(), oauthCreds)
	if out.Error != ErrTransient || out.Unanswered {
		t.Errorf("Refresh = %+v, want transient and not unanswered", out)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Refresh took %v, want about the send bound", took)
	}
}

// A reply later than the send bound but inside the reply bound is read.
func TestRefreshWaitsForReplyPastSendBound(t *testing.T) {
	c := boundedClient(slowTokenServer(t, 300*time.Millisecond), 50*time.Millisecond, 5*time.Second)
	out := c.Refresh(context.Background(), oauthCreds)
	if out.Credentials == "" || out.Error != "" || out.Unanswered {
		t.Errorf("Refresh = %+v, want the rotated credential", out)
	}
}

// A written request with no reply inside the reply bound may have spent the
// grant: the outcome says so.
func TestRefreshWithoutReplyIsUnanswered(t *testing.T) {
	c := boundedClient(slowTokenServer(t, -1), 50*time.Millisecond, 200*time.Millisecond)
	start := time.Now()
	out := c.Refresh(context.Background(), oauthCreds)
	if out.Error != ErrTransient || !out.Unanswered || out.Credentials != "" {
		t.Errorf("Refresh = %+v, want transient and unanswered", out)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Refresh took %v, want about the reply bound", took)
	}
}

func TestRefreshBoundDefaults(t *testing.T) {
	c := NewHTTPClient()
	if send, reply := c.refreshBounds(); send != DefaultRefreshSendTimeout || reply != DefaultRefreshReplyTimeout {
		t.Errorf("bounds = %v, %v; want %v, %v", send, reply, DefaultRefreshSendTimeout, DefaultRefreshReplyTimeout)
	}
	if DefaultRefreshSendTimeout != 5*time.Second || DefaultRefreshReplyTimeout != 30*time.Second {
		t.Errorf("defaults = %v, %v; want 5s, 30s", DefaultRefreshSendTimeout, DefaultRefreshReplyTimeout)
	}
}

// Only a parsed success or a 4xx is a verdict on a written request.
func TestRefreshWithoutVerdictIsUnanswered(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"5xx", 503, `upstream error`, true},
		{"undecodable 2xx", 200, `{"access_token": `, true},
		{"2xx without access token", 200, `{"expires_in": 3600}`, true},
		{"4xx without marker", 400, `{"error": "temporarily_unavailable"}`, false},
		{"invalid_grant", 400, `{"error": "invalid_grant"}`, false},
	} {
		ts := newTokenServer(t, tc.status, tc.body)
		out := clientFor(ts.srv.URL, "", "").Refresh(context.Background(), oauthCreds)
		if out.Credentials != "" || out.Unanswered != tc.want {
			t.Errorf("%s: Refresh = %+v, want unanswered %v", tc.name, out, tc.want)
		}
	}
}
