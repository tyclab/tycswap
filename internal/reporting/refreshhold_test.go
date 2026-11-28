package reporting

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
)

// A guarded refresh of the Fable store's login holds the store lock while it
// waits for Claude Code's locks and then for its reply, many times the store
// lock's timeout here; the active login's received successor waits until it
// has the lock and is stored (DESIGN A129). The bounds are small stand-ins
// for the real ones.
func TestReceivedSuccessorOutwaitsAGuardedRefreshWithNestedLocks(t *testing.T) {
	const (
		lockHold   = 700 * time.Millisecond
		send       = 200 * time.Millisecond
		reply      = time.Second
		replyDelay = 800 * time.Millisecond
	)
	s, active := refreshingStore(t, nil)
	writeSequenceRaw(t, s, `{"activeAccountNumber":1,"sequence":[1,2],"accounts":{"1":{"email":"a@example.com"},"2":{"email":"b@example.com"}}}`)
	// The Fable store is file-based; macOS refuses it, so the store is told it runs on Linux.
	s.Platform = platform.Linux
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetupFableDirectories(); err != nil {
		t.Fatal(err)
	}
	held := oauthCreds("fable-old-access", "fable-old-refresh", 1)
	if err := f.WriteAccountCredentials("2", "b@example.com", held); err != nil {
		t.Fatal(err)
	}
	if err := f.Creds.WriteActive(held); err != nil {
		t.Fatal(err)
	}
	credLock, err := cclock.AcquireProfileCredentials(paths.FableStoreDir(s.BackupDir()), 0, s.Clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credLock.Release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		time.Sleep(replyDelay)
		_, _ = io.WriteString(w, `{"access_token":"fable-new-access","refresh_token":"fable-new-refresh","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	client := &oauth.HTTPClient{Client: server.Client(), TokenURL: server.URL, RefreshSendTimeout: send, RefreshReplyTimeout: reply}
	s.Lock = filelock.New(s.LockFile, 100*time.Millisecond)
	worker := *s
	worker.Lock = filelock.New(s.LockFile, 0)
	done := make(chan oauth.RefreshOutcome, 1)
	go func() {
		done <- worker.RefreshFableLiveGuarded(context.Background(), client, "2", "b@example.com", held)
	}()
	for deadline := time.Now().Add(2 * time.Second); !worker.Lock.Held(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the guarded refresh did not take the store lock")
		}
	}
	time.AfterFunc(lockHold, credLock.Release)
	start := time.Now()
	rec := fetchActiveUsage(s, "1", skipEmail, active)
	waited := time.Since(start)
	if out := <-done; out.Credentials == "" {
		t.Fatalf("the guarded refresh failed: %q", out.Error)
	}
	if backup, _ := s.ReadAccountCredentials("1", skipEmail); rec.SkipReason != "" || !strings.Contains(backup, "new-refresh") {
		t.Errorf("the received successor was discarded after %s: skip %q, stored %v", waited.Round(time.Millisecond), rec.SkipReason, strings.Contains(backup, "new-refresh"))
	}
}
