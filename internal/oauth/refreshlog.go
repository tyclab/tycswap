package oauth

import (
	"context"

	"github.com/tyclab/tycswap/internal/logging"
)

// LoggedRefresh sends c's refresh of creds and logs it to l for subject
// ("account 3", no address, no token): INFO once the request is written,
// WARNING when it went out without a verdict and for invalid_grant, DEBUG for
// any other failure, a request never sent included (DESIGN A129).
func LoggedRefresh(ctx context.Context, c Client, l *logging.Logger, subject, creds string) RefreshOutcome {
	ctx = context.WithValue(ctx, refreshSentKey{}, func() { l.Infof("Refreshing the token of %s", subject) })
	out := c.Refresh(ctx, creds)
	switch {
	case out.Credentials != "":
	case out.Unanswered:
		l.Warningf("The token refresh of %s was sent but got no answer; its refresh token may be spent, and a later \"re-login needed\" for this account comes from this", subject)
	case out.Error == ErrInvalidGrant:
		l.Warningf("The token refresh of %s failed (%s)", subject, out.Error)
	default:
		l.Debugf("The token refresh of %s failed (%s)", subject, out.Error)
	}
	return out
}
