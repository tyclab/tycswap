package store

import (
	"context"

	"github.com/tyclab/tycswap/internal/oauth"
)

// RefreshDeclined is the RefreshOutcome error of a guarded refresh that was
// not attempted: the lock was busy, the slot became the live login or has a
// live session, or its backup is gone.
const RefreshDeclined = "refresh_declined"

// SendRefresh sends c's refresh of creds, subject's login ("account 3"), and
// logs it: INFO before, WARNING when the request went out and no verdict came
// (the grant may be spent) and for invalid_grant, DEBUG otherwise. The client
// bounds the request (oauth.DefaultRefreshSendTimeout, DESIGN A129).
func (s *Store) SendRefresh(ctx context.Context, c oauth.Client, subject, creds string) oauth.RefreshOutcome {
	s.Log.Infof("Refreshing the token of %s", subject)
	out := c.Refresh(ctx, creds)
	switch {
	case out.Credentials != "":
	case out.Unanswered:
		s.Log.Warningf("The token refresh of %s was sent but got no answer; its refresh token may be spent, and a later \"re-login needed\" for this account comes from this", subject)
	case out.Error == oauth.ErrInvalidGrant:
		s.Log.Warningf("The token refresh of %s failed (%s)", subject, out.Error)
	default:
		s.Log.Debugf("The token refresh of %s failed (%s)", subject, out.Error)
	}
	return out
}

// The lock is taken BEFORE the refresh: a rotated refresh token may die once issued, so an unpersistable refresh must not run.
// Under it the slot is re-checked and the backup re-read; a lineage that moved on is returned as stored, unrefreshed.
func (s *Store) RefreshBackupGuarded(ctx context.Context, c oauth.Client, num, email, held string) oauth.RefreshOutcome {
	s = s.Fresh()
	out := oauth.RefreshOutcome{Error: RefreshDeclined}
	err := s.Lock.With(func() error {
		owner, err := s.CredentialOwner(num)
		if err != nil || owner.Scope != "" {
			return nil
		}
		if cur := s.CurrentAccountNumber(); cur != nil && *cur == num {
			return nil // became the live login: Claude Code owns its token now
		}
		if len(s.LiveSessionPidsFor(num, email)) > 0 {
			return nil // a session in its former profile owns this lineage
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
		out = s.SendRefresh(ctx, c, "account "+num, backup)
		if out.Credentials == "" {
			return nil
		}
		if werr := s.WriteAccountCredentials(num, email, out.Credentials); werr != nil {
			if s.Log != nil {
				s.Log.Warningf("Refreshed the token for account %s but could not store it: %v", num, werr)
			}
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
