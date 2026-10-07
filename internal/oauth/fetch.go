package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/printer"
)

// outputSeam is the permanently-installed, concurrency-safe writer behind
// Output. Its destination is swapped atomically (see RedirectOutput) so the TUI
// can redirect the persist-failure warning while the auto-engine goroutine
// writes it concurrently, with no data race (FINDING 5). It starts at os.Stdout,
// matching printer.Warning's destination byte-for-byte so non-TUI behaviour is
// unchanged.
var outputSeam = newSyncWriter(os.Stdout)

// Output is where the user-visible persist-failure warning lands (04§1.25).
// Production writers reach the terminal through the atomically-swappable
// outputSeam; tests may replace Output wholesale for single-threaded capture.
var Output io.Writer = outputSeam

// syncWriter is an io.Writer whose destination can be swapped atomically. Writes
// and swaps are safe to interleave across goroutines: a Write always resolves to
// whichever destination is installed at that instant, and never tears.
type syncWriter struct {
	dst atomic.Pointer[io.Writer]
}

func newSyncWriter(w io.Writer) *syncWriter {
	sw := &syncWriter{}
	sw.dst.Store(&w)
	return sw
}

func (w *syncWriter) Write(p []byte) (int, error) { return (*w.dst.Load()).Write(p) }

// swap atomically installs next and returns the previously-installed writer.
func (w *syncWriter) swap(next io.Writer) io.Writer { return *w.dst.Swap(&next) }

// RedirectOutput atomically points the human-output seam at w and returns a
// closure that restores the previous destination. Both the redirect and the
// restore are atomic swaps, so it is safe to call concurrently with goroutines
// writing to Output (e.g. the auto-switch engine's persist-failure warning).
func RedirectOutput(w io.Writer) (restore func()) {
	prev := outputSeam.swap(w)
	return func() { outputSeam.swap(prev) }
}

// Log is the package logger seam, mirroring Python's module-level
// logging.getLogger("claude-swap"). When nil, WARNING/DEBUG lines are dropped.
// Store construction installs an oauth-backed logger.
var Log *logging.Logger

func debugf(format string, a ...any) {
	if Log != nil {
		Log.Debugf(format, a...)
	}
}

func warningf(format string, a ...any) {
	if Log != nil {
		Log.Warningf(format, a...)
	}
}

// PersistFn persists refreshed credentials for an account. It returns an error
// on failure, which the orchestration surfaces loudly without aborting the
// fetch.
type PersistFn func(num, email, creds string) error

// TryFetchUsageForAccount fetches usage for an account, proactively refreshing
// an expired token for INACTIVE accounts only (Claude Code owns the active
// account's credentials and must never be touched). Retries once after a
// refresh on a 401 for an inactive account with a refresh token.
func TryFetchUsageForAccount(
	ctx context.Context,
	c Client,
	num, email, creds string,
	isActive bool,
	persist PersistFn,
) UsageOutcome {
	return TryFetchUsageGuarded(ctx, c, num, email, creds, isActive, func(ctx context.Context, held string) RefreshOutcome {
		out := c.Refresh(ctx, held)
		if out.Credentials != "" {
			persistCredentials(persist, num, email, out.Credentials)
		}
		return out
	})
}

// GuardedRefresh refreshes held and persists the result before returning it.
// An implementation may decline (returning no Credentials) or answer with a
// newer credential it found on disk instead of refreshing; whatever it returns
// in Credentials must already be stored.
type GuardedRefresh func(ctx context.Context, held string) RefreshOutcome

// TryFetchUsageGuarded is TryFetchUsageForAccount with the refresh-and-persist
// step supplied by the caller, so it can run under the store lock with the
// backup re-read first (reporting's inactive path).
func TryFetchUsageGuarded(
	ctx context.Context,
	c Client,
	num, email, creds string,
	isActive bool,
	refreshFn GuardedRefresh,
) UsageOutcome {
	logCtx := "for account " + num // no email: paste-safe for public issues
	oauth := ExtractOAuthData(creds)
	accessToken := ""
	if oauth != nil {
		accessToken, _ = oauth["accessToken"].(string)
	}
	if accessToken == "" {
		return UsageOutcome{Error: ErrNoAccessToken}
	}

	working := creds

	if !isActive && oauth != nil && truthyStr(oauth["refreshToken"]) &&
		IsOAuthTokenExpired(oauth["expiresAt"], time.Now().UTC()) {
		refresh := refreshFn(ctx, working)
		if refresh.Credentials != "" {
			working = refresh.Credentials
			if o2 := ExtractOAuthData(working); o2 != nil {
				oauth = o2
			}
			if at, _ := oauth["accessToken"].(string); at != "" {
				accessToken = at
			}
		} else if refresh.Error == ErrInvalidGrant {
			return UsageOutcome{Error: ErrInvalidGrant}
		}
	}

	raw, err := c.Usage(ctx, accessToken)
	if err == nil {
		return UsageOutcome{Usage: BuildUsageResult(raw)}
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		kind, retryAfter := classifyUsageError(err)
		if httpErr.Code != 401 || isActive || oauth == nil || !truthyStr(oauth["refreshToken"]) {
			logUsageFailure(logCtx, err, kind, retryAfter)
			return UsageOutcome{Error: kind, RetryAfterS: retryAfter}
		}
		refresh := refreshFn(ctx, working)
		if refresh.Credentials == "" {
			logUsageFailure(logCtx, err, kind, nil)
			if refresh.Error == ErrInvalidGrant {
				return UsageOutcome{Error: ErrInvalidGrant}
			}
			return UsageOutcome{Error: ErrRefreshFailed}
		}
		working = refresh.Credentials
		newToken := ""
		if ro := ExtractOAuthData(working); ro != nil {
			newToken, _ = ro["accessToken"].(string)
		}
		if newToken == "" {
			return UsageOutcome{Error: ErrRefreshFailed}
		}
		raw2, err2 := c.Usage(ctx, newToken)
		if err2 == nil {
			return UsageOutcome{Usage: BuildUsageResult(raw2)}
		}
		kind2, retryAfter2 := classifyUsageError(err2)
		logUsageFailure(logCtx+" after refresh", err2, kind2, retryAfter2)
		return UsageOutcome{Error: kind2, RetryAfterS: retryAfter2}
	}

	kind, retryAfter := classifyUsageError(err)
	logUsageFailure(logCtx, err, kind, retryAfter)
	return UsageOutcome{Error: kind, RetryAfterS: retryAfter}
}

func FetchUsageForAccount(
	ctx context.Context,
	c Client,
	num, email, creds string,
	isActive bool,
	persist PersistFn,
) map[string]any {
	return TryFetchUsageForAccount(ctx, c, num, email, creds, isActive, persist).Usage
}

func FetchUsage(ctx context.Context, c Client, accessToken string) map[string]any {
	raw, err := c.Usage(ctx, accessToken)
	if err != nil {
		kind, _ := classifyUsageError(err)
		logUsageFailure("", err, kind, nil)
		return nil
	}
	return BuildUsageResult(raw)
}

// logUsageFailure emits one WARNING line (so failures land in the default log)
// plus the exception detail at DEBUG (04§1.17). The context must never carry
// the email. Retry-After rides along rounded to whole seconds; the 429 case
// appends the budget note.
func logUsageFailure(logCtx string, err error, kind string, retryAfterS *float64) {
	where := ""
	if logCtx != "" {
		where = " " + logCtx
	}
	cause := kind
	if retryAfterS != nil {
		cause = fmt.Sprintf("%s, retry-after %.0fs", kind, *retryAfterS)
	}
	if kind == "http-429" {
		cause += " (per-token usage budget reached; backing off)"
	}
	warningf("Usage fetch failed%s: %s", where, cause)
	debugf("Usage fetch failure detail%s: %v", where, err)
}

// persistCredentials calls the persist callback, warning loudly on failure via
// both the internal log (with email) and a user-visible warning written to the
// package-level Output seam (os.Stdout by default, 04§1.25). A nil callback is a
// no-op.
func persistCredentials(persist PersistFn, num, email, creds string) {
	if persist == nil {
		return
	}
	if err := persist(num, email, creds); err != nil {
		warningf(
			"Refreshed OAuth token for account %s (%s) but failed to persist it: %v. "+
				"The refresh token on disk may now be stale; if the next refresh fails "+
				"with invalid_grant, re-run `tycswap --add-account` after logging in.",
			num, email, err,
		)
		fmt.Fprintln(Output, printer.Yellowed(fmt.Sprintf(
			"Warning: failed to save refreshed token for account %s (%s). "+
				"If the next refresh fails, re-run `tycswap --add-account` after logging in.",
			num, email,
		)))
	}
}
