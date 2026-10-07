package autoswitch

import (
	"context"
	"strings"

	"github.com/tyclab/tycswap/internal/oauth"
)

func (e *Engine) freshenTarget(number, email string) string {
	if e.sw.AccountKindFor(number) == "api_key" {
		return "ok" // API keys don't expire/refresh
	}
	if len(e.sw.LiveSessionPidsFor(number, email)) > 0 {
		// A live `tycswap run` session owns this account's token in its own
		// profile; auto-activating it as the default too would duplicate a
		// rotating refresh token with nobody reading the warning.
		return "skip-live-session"
	}
	creds := e.sw.ReadAccountCredentials(number, email)
	if creds == "" {
		return "transient"
	}
	data := oauth.ExtractOAuthData(creds)
	if data == nil {
		return "invalid_grant"
	}
	nowMs := e.nowSeconds() * 1000
	nearExpiry := false
	if ms, ok := numOfAny(data["expiresAt"]); ok {
		nearExpiry = nowMs+FreshenBufferMS >= ms
	}
	if !nearExpiry {
		return "ok" // fresh token, no refresh
	}
	// Under the lock: the slot is re-checked and the backup re-read; a
	// lineage another process moved on comes back as stored, a refreshed one
	// is persisted before it is returned, and a declined refresh (lock busy,
	// slot became live) is a transient outcome.
	outcome := e.sw.RefreshBackupGuarded(context.Background(), e.oauth, number, email, creds)
	if outcome.Error == "" && outcome.Credentials != "" {
		if e.noteTokenIdentity(number, outcome.TokenAccount) {
			return "identity-conflict"
		}
		return "ok"
	}
	if outcome.Error == oauth.ErrInvalidGrant || outcome.Error == oauth.ErrNoRefreshToken {
		return "invalid_grant"
	}
	return "transient"
}

// noteTokenIdentity verifies/backfills a slot from the refresh grant's free
// identity; returns true on a conflict (05§12). Org is compared first (whenever
// both sides record one); a blank slot uuid is backfilled only when no org
// conflict exists (a wrong-org credential must not poison the slot's identity).
func (e *Engine) noteTokenIdentity(number string, ta *oauth.Identity) bool {
	if ta == nil {
		return false
	}
	taUUID := strings.TrimSpace(ta.UUID)
	if taUUID == "" {
		return false
	}
	slot := e.sw.AccountIdentity(number)
	taOrg := ta.OrgUUID
	slotOrg := slot["organizationUuid"]
	if taOrg != "" && slotOrg != "" && taOrg != slotOrg {
		return true // same-uuid-different-org is still a conflict
	}
	if slot["uuid"] == "" {
		// Backfill onto a blank-uuid slot (never rewrites a non-empty uuid).
		e.sw.BackfillAccountUUID(number, taUUID)
		return false
	}
	return slot["uuid"] != taUUID
}
