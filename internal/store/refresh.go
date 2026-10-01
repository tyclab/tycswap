// refresh.go — the guarded refresh of an inactive slot's backup credential:
// the one way a token that is not the live login's is refreshed (DESIGN A25
// item 4). The usage fetch (reporting) and the auto-switch freshen (autoswitch)
// both go through it.
package store

import (
	"context"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
)

// RefreshDeclined is the RefreshOutcome error of a guarded refresh that was
// not attempted: the lock was busy, the slot became the live login or has a
// live session, or its backup is gone.
const RefreshDeclined = "refresh_declined"

// GuardedRefreshTimeout bounds the HTTP refresh performed under the store
// lock. It is below the lock's own timeout (filelock.DefaultTimeout, 10 s), so
// a command waiting for the lock while a refresh runs gets it before it gives
// up.
const GuardedRefreshTimeout = 5 * time.Second

// RefreshBackupGuarded refreshes slot num's backup credential with c, under
// the store lock, and persists the result before returning it; held is the
// credential the caller read before (modelled on the Codex switcher). A
// rotated refresh token may die the moment the response is issued, so a
// refresh that cannot be persisted must not be performed: the lock is taken
// BEFORE the refresh, not around the persist alone. Under the lock everything
// read earlier is re-checked: the slot must still be inactive with no live
// session, and the backup is re-read. If its refresh token is no longer
// held's, another process refreshed it (or a switch wrote it back) and that
// newer credential is returned as stored, with no refresh; only an unchanged
// lineage is refreshed and written back. Whatever comes back in Credentials
// is already in the store.
func (s *Store) RefreshBackupGuarded(ctx context.Context, c oauth.Client, num, email, held string) oauth.RefreshOutcome {
	out := oauth.RefreshOutcome{Error: RefreshDeclined}
	err := s.Lock.With(func() error {
		if cur := s.CurrentAccountNumber(); cur != nil && *cur == num {
			return nil // became the live login: Claude Code owns its token now
		}
		if len(s.LiveSessionPidsFor(num, email)) > 0 {
			return nil // a `tycswap run` session owns this lineage
		}
		backup, _ := s.ReadAccountCredentials(num, email)
		if backup == "" {
			return nil
		}
		if !fingerprintsEqual(backup, held) {
			// Someone else moved the lineage on. Their credential is on disk
			// already; use it if it is a credential at all.
			if oauth.ExtractAccessToken(backup) != "" {
				out = oauth.RefreshOutcome{Credentials: backup}
			}
			return nil
		}
		reqCtx, cancel := context.WithTimeout(ctx, GuardedRefreshTimeout)
		defer cancel()
		out = c.Refresh(reqCtx, backup)
		if out.Credentials == "" {
			return nil
		}
		if werr := s.WriteAccountCredentials(num, email, out.Credentials); werr != nil {
			if s.Log != nil {
				s.Log.Warningf("Refreshed the token for account %s but could not store it: %v", num, werr)
			}
			// Serve nothing the store does not hold.
			out = oauth.RefreshOutcome{Error: oauth.ErrRefreshFailed}
		}
		return nil
	})
	if err != nil && s.Log != nil {
		s.Log.Debugf("Skipped the token refresh for account %s: %v", num, err)
	}
	return out
}

// fingerprintsEqual reports whether two credentials share a refresh-token
// lineage.
func fingerprintsEqual(a, b string) bool {
	fa := oauth.CredentialFingerprint(a)
	fb := oauth.CredentialFingerprint(b)
	return fa != nil && fb != nil && *fa == *fb
}
