// Tests for the guarded refresh's reply wait and its log lines (DESIGN A129).
package store

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
)

// twoSlotStore: slot 1 is the live login, slot 2 an idle backup.
func twoSlotStore(t *testing.T) *Store {
	t.Helper()
	s := freshStore(t)
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [1, 2],
  "accounts": {
    "1": {"email": "user1@example.com", "uuid": "", "organizationUuid": "org-one", "organizationName": "One", "added": "t"},
    "2": {"email": "user2@example.com", "uuid": "", "organizationUuid": "org-two", "organizationName": "Two", "added": "t"}
  }
}`)
	for num, email := range map[string]string{"1": "user1@example.com", "2": "user2@example.com"} {
		if err := s.Creds.WriteBackup(num, email, lineageCreds("at-"+num, "rt-"+num)); err != nil {
			t.Fatal(err)
		}
		writeBackupConfig(t, s, num, email, `{"oauthAccount":{}}`)
	}
	writeLiveCreds(t, s, lineageCreds("at-1", "rt-1"))
	writeLiveIdentity(t, s, "user1@example.com", "org-one")
	return s
}

// delayedTokenServer replies with a rotated login after delay; a negative
// delay never replies before the client gives up.
func delayedTokenServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		var wait <-chan time.Time
		if delay >= 0 {
			wait = time.After(delay)
		}
		select {
		case <-wait:
		case <-r.Context().Done():
			return
		case <-release:
			return
		}
		_, _ = io.WriteString(w, `{"access_token": "at-2-new", "refresh_token": "rt-2-new", "expires_in": 3600}`)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv.URL
}

func TestGuardedRefreshStoresReplyAfterSendBound(t *testing.T) {
	s := twoSlotStore(t)
	held := lineageCreds("at-2", "rt-2")
	c := &oauth.HTTPClient{Client: &http.Client{}, TokenURL: delayedTokenServer(t, 300*time.Millisecond),
		RefreshSendTimeout: 50 * time.Millisecond, RefreshReplyTimeout: 5 * time.Second}
	out := s.RefreshBackupGuarded(context.Background(), c, "2", "user2@example.com", held)
	if out.Credentials == "" {
		t.Fatalf("RefreshBackupGuarded = %+v, want the successor", out)
	}
	if backup, _ := s.ReadAccountCredentials("2", "user2@example.com"); !strings.Contains(backup, "rt-2-new") {
		t.Errorf("slot 2's backup does not hold the successor")
	}
	if log := logText(t, s); !strings.Contains(log, "INFO - Refreshing the token of account 2") {
		t.Errorf("log lacks the INFO line before the refresh:\n%s", log)
	}
}

func TestGuardedRefreshWarnsWhenReplyLost(t *testing.T) {
	s := twoSlotStore(t)
	held := lineageCreds("at-2", "rt-2")
	c := &oauth.HTTPClient{Client: &http.Client{}, TokenURL: delayedTokenServer(t, -1),
		RefreshSendTimeout: 50 * time.Millisecond, RefreshReplyTimeout: 200 * time.Millisecond}
	out := s.RefreshBackupGuarded(context.Background(), c, "2", "user2@example.com", held)
	if out.Credentials != "" || !out.Unanswered {
		t.Fatalf("RefreshBackupGuarded = %+v, want an unanswered refresh", out)
	}
	if backup, _ := s.ReadAccountCredentials("2", "user2@example.com"); backup != held {
		t.Errorf("slot 2's backup changed")
	}
	log := logText(t, s)
	if !strings.Contains(log, "WARNING - The token refresh of account 2 was sent but got no answer") ||
		!strings.Contains(log, "re-login needed") {
		t.Errorf("log lacks the lost-reply warning:\n%s", log)
	}
}

func TestGuardedRefreshLogsFailureReason(t *testing.T) {
	s := twoSlotStore(t)
	c := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
	}}
	s.RefreshBackupGuarded(context.Background(), c, "2", "user2@example.com", lineageCreds("at-2", "rt-2"))
	if log := logText(t, s); !strings.Contains(log, "WARNING - The token refresh of account 2 failed (invalid_grant)") {
		t.Errorf("log lacks the failure's reason:\n%s", log)
	}
}
