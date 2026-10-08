// oauth.go — refreshing Codex access tokens (claude-swap PR #252 codex/oauth.py). codex-auth leaves refresh to the codex CLI;
// tycswap refreshes because autoswitch compares accounts nobody opened in hours, and a stale token answers 401.
//
// Established against the live endpoint with an invalid refresh token (2026-08-16): POST takes a JSON body; failures answer 401
// with a nested {"error": {"code": "token_expired"}}, not RFC 6749's flat {"error": "invalid_grant"}; both shapes are parsed.
// client_id app_EMoamEEZ73f0CkXaXp7hrann is the public client shipped in the codex binary, not a secret. The binary also
// carries app_69a1d78e929881919bba0dbda1f6436d, which this endpoint rejects with invalid_client: do not "fix" the id back.
// Nothing here is documented, so every failure degrades: the account shows its status and drops out of autoswitch.
//
// The active account is never refreshed from a stored snapshot: the codex CLI holds that refresh token, and a parallel
// refresh can invalidate the one the server rotates away from, which is a logout. Callers pass the LIVE payload for it.
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

const (
	OAuthTokenURL = "https://auth.openai.com/oauth/token"
	// Verified against the live endpoint; read the file header before changing it.
	OAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	OAuthScope    = "openid profile email offline_access"
	UsageURL      = "https://chatgpt.com/backend-api/wham/usage"
	AccountsURL   = "https://chatgpt.com/backend-api/accounts"
	UserAgent     = "claude-swap/1.0"
	// A token expiring mid-request is indistinguishable from a revoked one at the call site.
	ExpiryMarginS    = 120.0
	WeeklyWindowMinS = 86400
)

const (
	KindOK             = ""
	KindTransient      = "transient"
	KindNotApplicable  = "not_applicable"
	KindNoRefreshToken = "no_refresh_token"
	KindInvalidGrant   = "invalid_grant"
	KindInvalidClient  = "invalid_client"
	// This endpoint's wording for a dead refresh token: the account needs a fresh login.
	KindTokenExpired = "token_expired"
)

// Unlisted codes stay transient: a misclassified transient costs one retry, a misclassified permanent quarantines a live account.
var permanentErrors = map[string]bool{
	KindInvalidGrant:  true,
	KindInvalidClient: true,
	KindTokenExpired:  true,
}

type RefreshOutcome struct {
	Payload map[string]any
	Kind    string
}

// An unreadable expiry counts as needing refresh: a pointless refresh costs one request, a skipped one a blank usage row.
func NeedsRefresh(payload any, now float64) bool {
	exp := authfile.AccessTokenExpiry(payload)
	if exp == nil {
		return true
	}
	return *exp-now <= ExpiryMarginS
}

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
		// Status and code only: the request carries a refresh token and the response can echo request context.
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
		// A 200 without a token is no success: persisting it would keep a stale access token beside a possibly spent refresh token.
		debugf("Codex token refresh failed: response carried no access token")
		return RefreshOutcome{Kind: KindTransient}
	}
	return RefreshOutcome{Payload: applyRefresh(payload, tokens, data, time.Now().UTC())}
}

// An absent refresh_token means keep the old one (RFC 6749 §6): overwriting it with null destroys the only way back.
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
