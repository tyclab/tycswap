// oauth.go — refreshing Codex access tokens. Ports claude-swap PR #252
// codex/oauth.py.
//
// codex-auth does not do this: it leaves refresh to the codex CLI and renders
// the resulting HTTP status in its usage column. tycswap refreshes, because
// autoswitch has to compare accounts nobody has opened in hours, and a stale
// token answers 401 instead of a percentage.
//
// Endpoint, client id and error shape were established empirically against the
// live endpoint with a deliberately invalid refresh token (2026-08-16):
//
//   - POST https://auth.openai.com/oauth/token accepts a JSON body.
//   - client_id app_EMoamEEZ73f0CkXaXp7hrann is the public OAuth client shipped
//     inside the publicly distributed codex binary. It identifies the app, it
//     does not authenticate it, and it is not a secret. (The binary also carries
//     app_69a1d78e929881919bba0dbda1f6436d, which this endpoint rejects with
//     invalid_client; it belongs to something else. Do not "fix" the id back.)
//   - Failures answer 401, not 400, and the body is nested —
//     {"error": {"code": "token_expired", ...}} — not RFC 6749's flat
//     {"error": "invalid_grant"}. Both shapes are parsed, since the flat one is
//     what the standard specifies and this endpoint is undocumented.
//
// Because nothing here is documented, every failure mode degrades rather than
// raises: the account renders its status and drops out of autoswitch
// candidacy, and the rest of tycswap keeps working.
//
// The active account is never refreshed from a stored snapshot. The codex CLI
// holds its own copy of that refresh token and keeps auth.json current;
// refreshing our copy in parallel risks invalidating whichever token the server
// rotates away from, and that is a logout. Callers pass the LIVE payload for
// the active account and snapshots only for inactive ones.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/tyclab/tycswap/internal/codex/authfile"
)

// Endpoints, the public client, and the shared tuning constants.
const (
	OAuthTokenURL = "https://auth.openai.com/oauth/token"
	// OAuthClientID is the Codex CLI's public OAuth client, verified against
	// the live endpoint — read the file header before changing it.
	OAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	OAuthScope    = "openid profile email offline_access"
	UsageURL      = "https://chatgpt.com/backend-api/wham/usage"
	AccountsURL   = "https://chatgpt.com/backend-api/accounts"
	UserAgent     = "claude-swap/1.0"
	// ExpiryMarginS refreshes this far before nominal expiry: a token that
	// expires mid-request is indistinguishable from a revoked one at the call
	// site.
	ExpiryMarginS = 120.0
	// WeeklyWindowMinS is the length at or above which a rate-limit window is
	// tycswap's weekly (seven_day) window; anything shorter is the 5-hour one. One
	// day is a wide moat between the two real values (5 h and 7 d).
	WeeklyWindowMinS = 86400
)

// Refresh outcome kinds. "" (KindOK) is success; KindTransient means try again
// later; everything else is a verdict about this account that will not improve
// by retrying.
const (
	KindOK             = ""
	KindTransient      = "transient"
	KindNotApplicable  = "not_applicable"
	KindNoRefreshToken = "no_refresh_token"
	KindInvalidGrant   = "invalid_grant"
	KindInvalidClient  = "invalid_client"
	// KindTokenExpired is this endpoint's wording for a dead REFRESH token: the
	// account needs a fresh login.
	KindTokenExpired = "token_expired"
)

// permanentErrors are the server verdicts that will not improve by retrying.
// Anything not listed stays transient: a misclassified transient costs one
// retry, a misclassified permanent quarantines a live account.
var permanentErrors = map[string]bool{
	KindInvalidGrant:  true,
	KindInvalidClient: true,
	KindTokenExpired:  true,
}

// RefreshOutcome is the result of one refresh attempt. Payload is the updated
// auth.json payload on success (Kind == ""), else nil.
type RefreshOutcome struct {
	Payload map[string]any
	Kind    string
}

// NeedsRefresh reports whether payload's access token is expired or within
// ExpiryMarginS of expiring at now (POSIX seconds). An unreadable expiry
// counts as needing refresh: a pointless refresh costs one request, a skipped
// one costs a blank usage row.
func NeedsRefresh(payload any, now float64) bool {
	exp := authfile.AccessTokenExpiry(payload)
	if exp == nil {
		return true
	}
	return *exp-now <= ExpiryMarginS
}

// errorCode extracts the server's error code from either body shape: this
// endpoint nests it ({"error": {"code": ...}}, falling back to "type"), RFC
// 6749 puts a bare string there. Undocumented endpoints change, so both are
// read. Returns "" when the body carries neither.
func errorCode(body []byte) string {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	switch e := doc["error"].(type) {
	case string:
		return e
	case map[string]any:
		code := e["code"]
		if !truthy(code) {
			code = e["type"]
		}
		s, _ := code.(string)
		return s
	}
	return ""
}

// TryRefresh exchanges the payload's refresh token for a fresh access token.
// The given payload is never mutated — the caller may still need it if the
// persist fails. Persisting the returned payload is the caller's job, and it
// must happen immediately and under the store lock: a rotated refresh token
// that is never written down is an account lost.
func (c *HTTPClient) TryRefresh(ctx context.Context, payload map[string]any) RefreshOutcome {
	if payload == nil {
		return RefreshOutcome{Kind: KindTransient}
	}
	tokens, ok := payload["tokens"].(map[string]any)
	if !ok {
		if payload["auth_mode"] == "apikey" || truthy(payload["OPENAI_API_KEY"]) {
			return RefreshOutcome{Kind: KindNotApplicable}
		}
		return RefreshOutcome{Kind: KindTransient}
	}
	refreshToken := tokens["refresh_token"]
	if !truthy(refreshToken) {
		return RefreshOutcome{Kind: KindNoRefreshToken}
	}

	// The verified request: JSON body, public client id, and the scope the
	// codex CLI itself asks for.
	body, err := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     OAuthClientID,
		"scope":         OAuthScope,
	})
	if err != nil {
		debugf("Codex token refresh failed: encode")
		return RefreshOutcome{Kind: KindTransient}
	}

	reqCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.TokenURL, bytes.NewReader(body))
	if err != nil {
		debugf("Codex token refresh failed: request")
		return RefreshOutcome{Kind: KindTransient}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		debugf("Codex token refresh failed: %s", transportKind(err))
		return RefreshOutcome{Kind: KindTransient}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		// Status and the server's code only: the request carries a refresh
		// token and the response can echo request context.
		code := errorCode(raw)
		shown := code
		if shown == "" {
			shown = "None"
		}
		debugf("Codex token refresh failed: http-%d (%s)", resp.StatusCode, shown)
		switch resp.StatusCode {
		case 400, 401, 403:
			if permanentErrors[code] {
				return RefreshOutcome{Kind: code}
			}
		}
		return RefreshOutcome{Kind: KindTransient}
	}

	decoded, err := decodeJSON(io.LimitReader(resp.Body, maxSuccessBody))
	if err != nil {
		debugf("Codex token refresh failed: undecodable response")
		return RefreshOutcome{Kind: KindTransient}
	}
	data, ok := decoded.(map[string]any)
	if !ok || !truthy(data["access_token"]) {
		// A 200 that carries no token is not a success: treating it as one
		// would persist a stale access token beside a possibly-spent refresh
		// token.
		debugf("Codex token refresh failed: response carried no access token")
		return RefreshOutcome{Kind: KindTransient}
	}
	return RefreshOutcome{Payload: applyRefresh(payload, tokens, data, time.Now().UTC())}
}

// applyRefresh builds the updated payload from a successful token response,
// copying rather than mutating the caller's maps. An absent refresh_token
// means "keep using the one you have" (RFC 6749 §6): overwriting it with null
// would destroy the account's only way back.
func applyRefresh(payload, tokens, data map[string]any, now time.Time) map[string]any {
	updated := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		updated[k] = v
	}
	newTokens := make(map[string]any, len(tokens)+1)
	for k, v := range tokens {
		newTokens[k] = v
	}
	newTokens["access_token"] = data["access_token"]
	if truthy(data["id_token"]) {
		newTokens["id_token"] = data["id_token"]
	}
	if truthy(data["refresh_token"]) {
		newTokens["refresh_token"] = data["refresh_token"]
	}
	updated["tokens"] = newTokens
	updated["last_refresh"] = now.UTC().Format("2006-01-02T15:04:05Z")
	return updated
}
