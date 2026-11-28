// Tests for the guarded refresh's reply wait and its log lines (DESIGN A129).
package store

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
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
	log := logText(t, s)
	if !strings.Contains(log, "INFO - Refreshing the token of account 2") {
		t.Errorf("log lacks the INFO line of the sent refresh:\n%s", log)
	}
	noTokens(t, log, "at-2", "rt-2", "at-2-new", "rt-2-new")
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

// A refresh whose request never went out writes nothing above DEBUG: an
// offline box stays quiet (ruling 18), the INFO line means "sent".
func TestGuardedRefreshNeverSentWritesNothingAboveDebug(t *testing.T) {
	s := twoSlotStore(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	c := &oauth.HTTPClient{Client: &http.Client{}, TokenURL: "https://" + ln.Addr().String() + "/token",
		RefreshSendTimeout: 50 * time.Millisecond, RefreshReplyTimeout: time.Second}
	if out := s.RefreshBackupGuarded(context.Background(), c, "2", "user2@example.com", lineageCreds("at-2", "rt-2")); out.Unanswered || out.Error != oauth.ErrTransient {
		t.Fatalf("RefreshBackupGuarded = %+v, want a transient failure that was never sent", out)
	}
	if log := logText(t, s); strings.Contains(log, "INFO") || strings.Contains(log, "WARNING") {
		t.Errorf("an unsent refresh wrote a line above DEBUG:\n%s", log)
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
	noTokens(t, log, "at-2", "rt-2")
}

func TestGuardedRefreshLogsFailureReason(t *testing.T) {
	s := twoSlotStore(t)
	c := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
		return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
	}}
	s.RefreshBackupGuarded(context.Background(), c, "2", "user2@example.com", lineageCreds("at-2", "rt-2"))
	log := logText(t, s)
	if !strings.Contains(log, "WARNING - The token refresh of account 2 failed (invalid_grant)") {
		t.Errorf("log lacks the failure's reason:\n%s", log)
	}
	noTokens(t, log, "at-2", "rt-2")
}

// noCallerDeadline is a client that fails the test when the context a guarded
// refresh hands it ends before the client's own send and reply bounds: the
// client bounds a refresh, its callers do not (DESIGN A129).
func noCallerDeadline(t *testing.T, fresh string) *oauth.FakeClient {
	return &oauth.FakeClient{RefreshFn: func(ctx context.Context, _ string) oauth.RefreshOutcome {
		if at, ok := ctx.Deadline(); ok && time.Until(at) < oauth.DefaultRefreshSendTimeout+oauth.DefaultRefreshReplyTimeout {
			t.Errorf("the caller ends the refresh %s from now, before the client's own bounds", time.Until(at).Round(time.Millisecond))
		}
		return oauth.RefreshOutcome{Credentials: fresh}
	}}
}

// Each guarded refresh hands the client the caller's context, with no
// deadline of its own around the request.
func TestGuardedRefreshesPutNoDeadlineOnTheClient(t *testing.T) {
	t.Run("default login", func(t *testing.T) {
		s := twoSlotStore(t)
		fresh := lineageCreds("at-2b", "rt-2b")
		if out := s.RefreshBackupGuarded(context.Background(), noCallerDeadline(t, fresh), "2", "user2@example.com", lineageCreds("at-2", "rt-2")); out.Credentials != fresh {
			t.Fatalf("RefreshBackupGuarded = %+v, want the successor", out)
		}
	})
	t.Run("Fable login", func(t *testing.T) {
		s := fableFixture(t)
		held, fresh := fableCreds("at-f2", "rt-f2"), fableCreds("at-f2b", "rt-f2b")
		putFableLogin(t, s, "2", held)
		if out := s.RefreshFableBackupGuarded(context.Background(), noCallerDeadline(t, fresh), "2", fableEmails["2"], held); out.Credentials != fresh {
			t.Fatalf("RefreshFableBackupGuarded = %+v, want the successor", out)
		}
	})
	t.Run("Fable store's live login", func(t *testing.T) {
		s := fableFixture(t)
		held, fresh := fableCreds("at-f2", "rt-f2"), fableCreds("at-f2b", "rt-f2b")
		putFableLogin(t, s, "2", held)
		putFableLive(t, s, held)
		if out := s.RefreshFableLiveGuarded(context.Background(), noCallerDeadline(t, fresh), "2", fableEmails["2"], held); out.Credentials != fresh {
			t.Fatalf("RefreshFableLiveGuarded = %+v, want the successor", out)
		}
	})
	t.Run("session profile login", func(t *testing.T) {
		s, _ := claimedSession(t)
		s.Platform = platform.Linux
		const login = `{"claudeAiOauth":{"accessToken":"old-access","refreshToken":"old-refresh"}}`
		const fresh = `{"claudeAiOauth":{"accessToken":"new-access","refreshToken":"new-refresh","expiresAt":4102444800000}}`
		err := s.Lock.With(func() error {
			out, err := s.RefreshLegacyLogin(context.Background(), noCallerDeadline(t, fresh), "1", login)
			if err == nil && out.Credentials != fresh {
				t.Errorf("RefreshLegacyLogin = %+v, want the successor", out)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
