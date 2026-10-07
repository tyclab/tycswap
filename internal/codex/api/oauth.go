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

const (
	OAuthTokenURL    = "https://auth.openai.com/oauth/token"
	OAuthClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	OAuthScope       = "openid profile email offline_access"
	UsageURL         = "https://chatgpt.com/backend-api/wham/usage"
	AccountsURL      = "https://chatgpt.com/backend-api/accounts"
	UserAgent        = "claude-swap/1.0"
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
	KindTokenExpired   = "token_expired"
)

var permanentErrors = map[string]bool{
	KindInvalidGrant:  true,
	KindInvalidClient: true,
	KindTokenExpired:  true,
}

type RefreshOutcome struct {
	Payload map[string]any
	Kind    string
}

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
		debugf("Codex token refresh failed: response carried no access token")
		return RefreshOutcome{Kind: KindTransient}
	}
	return RefreshOutcome{Payload: applyRefresh(payload, tokens, data, time.Now().UTC())}
}

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
