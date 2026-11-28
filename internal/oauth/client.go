// Network client: the Client seam, its real HTTP implementation, and a fake.
//
// Implements spec 04§1.8 (try_refresh_oauth_credentials), 04§1.9 (refresh error
// classification), 04§1.10 (_parse_token_account), 04§1.12 (fetch_oauth_profile),
// 04§1.15 (request_usage_data). Two hosts: refresh -> platform.claude.com,
// profile/usage -> api.anthropic.com. Timeouts: refresh 5s to send then 30s
// for the reply (DESIGN A129), profile/usage 5s.

package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Identity is an account identity resolved from the token endpoint
// (_parse_token_account) or the profile endpoint. Empty string means absent
// (Python None) for the optional Email/OrgUUID fields.
type Identity struct {
	UUID    string
	Email   string
	OrgUUID string
}

// RefreshOutcome is the result of a refresh-token grant attempt (04§1.7).
// Credentials holds the full rotated credentials JSON on success (else ""),
// Error classifies failures (ErrInvalidGrant / ErrNoRefreshToken / ErrTransient;
// "" on success), and TokenAccount is the opportunistic identity or nil.
// Unanswered marks a transient failure after the request was written with no
// verdict read (a parsed success or a 4xx): the server may have spent the
// grant, so its successor may be lost (DESIGN A129).
type RefreshOutcome struct {
	Credentials  string
	Error        string
	TokenAccount *Identity
	Unanswered   bool
}

// UsageOutcome is the result of a usage-API fetch attempt (04§1.21). Usage is
// the normalized usage map on success (nil when the round trip carried no
// window data), Error is "" on success else a classification token, and
// RetryAfterS carries the server Retry-After when present.
type UsageOutcome struct {
	Usage       map[string]any
	Error       string
	RetryAfterS *float64
}

// Client is the network seam for OAuth/usage I/O.
type Client interface {
	Refresh(ctx context.Context, creds string) RefreshOutcome
	Profile(ctx context.Context, accessToken string) *Identity
	Usage(ctx context.Context, accessToken string) (raw map[string]any, err error)
}

// ProfileProber is an optional Client capability: ProfileAnswer resolves a
// token as Profile does and reports whether the endpoint answered (an
// identity, 401 or 403) or the request failed (transport error, timeout,
// 429, 5xx, malformed reply). A Client without it cannot tell: its nil
// counts as a failed request.
type ProfileProber interface {
	ProfileAnswer(ctx context.Context, accessToken string) (id *Identity, answered bool)
}

// HTTPClient is the real Client backed by net/http. The URL fields default to
// the production endpoints and are overridable for tests. A zero refresh
// bound takes its default.
type HTTPClient struct {
	Client              *http.Client
	TokenURL            string
	ProfileURL          string
	UsageURL            string
	RefreshSendTimeout  time.Duration
	RefreshReplyTimeout time.Duration
}

// A refresh runs under the store lock, which a waiting command gives up on
// after filelock.DefaultTimeout (10 s). Until its request is written it is
// bounded below that, and a failure spent nothing. Once written, its reply is
// waited for as long as Claude Code waits: giving up may lose the successor.
const (
	DefaultRefreshSendTimeout  = 5 * time.Second
	DefaultRefreshReplyTimeout = 30 * time.Second
)

func (c *HTTPClient) refreshBounds() (send, reply time.Duration) {
	send, reply = c.RefreshSendTimeout, c.RefreshReplyTimeout
	if send <= 0 {
		send = DefaultRefreshSendTimeout
	}
	if reply <= 0 {
		reply = DefaultRefreshReplyTimeout
	}
	return send, reply
}

// refreshPhase cancels a refresh at the send bound until the request is
// written, then at the reply bound, and remembers whether it was written.
type refreshPhase struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel context.CancelFunc
	reply  time.Duration
	sent   bool
}

func startRefreshPhase(ctx context.Context, send, reply time.Duration) (context.Context, *refreshPhase) {
	reqCtx, cancel := context.WithCancel(ctx)
	p := &refreshPhase{cancel: cancel, reply: reply}
	p.timer = time.AfterFunc(send, cancel)
	return httptrace.WithClientTrace(reqCtx, &httptrace.ClientTrace{WroteRequest: p.wrote}), p
}

func (p *refreshPhase) wrote(info httptrace.WroteRequestInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if info.Err != nil || p.sent {
		return
	}
	p.sent = true
	if p.timer.Stop() {
		p.timer = time.AfterFunc(p.reply, p.cancel)
	}
}

func (p *refreshPhase) written() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sent
}

func (p *refreshPhase) stop() {
	p.mu.Lock()
	p.timer.Stop()
	p.mu.Unlock()
	p.cancel()
}

// maxSuccessBody caps a decoded success response, as the Codex client does.
const maxSuccessBody = 1 << 20

// maxExpiresIn bounds a token lifetime in seconds (about 115 days); anything
// at or above it, or not positive, is a malformed response.
const maxExpiresIn = 1e7

// newSafeHTTPClient never follows a redirect: a 307/308 from the token
// endpoint would re-POST the refresh token to whatever Location names, an
// http:// one included. The redirect response itself is returned and treated
// as a failure.
func newSafeHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var safeDefaultClient = newSafeHTTPClient()

// NewHTTPClient returns an HTTPClient pointed at the production endpoints.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		Client:     newSafeHTTPClient(),
		TokenURL:   OAuthTokenURL,
		ProfileURL: profileURL,
		UsageURL:   usageURL,
	}
}

func (c *HTTPClient) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return safeDefaultClient
}

// Refresh performs a refresh-token grant (04§1.8/§1.9). No scope field is ever
// sent. Preserves unknown top-level credential fields verbatim across the
// rotation (only the nested claudeAiOauth object is mutated).
func (c *HTTPClient) Refresh(ctx context.Context, creds string) RefreshOutcome {
	data, ok := decodeCredsMap(creds)
	if !ok {
		return RefreshOutcome{Error: ErrNoRefreshToken}
	}
	oauth, ok := data["claudeAiOauth"].(map[string]any)
	if !ok {
		return RefreshOutcome{Error: ErrNoRefreshToken}
	}
	refreshToken, ok := oauth["refreshToken"].(string)
	if !ok || refreshToken == "" {
		return RefreshOutcome{Error: ErrNoRefreshToken}
	}

	body, _ := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     OAuthClientID,
	})

	send, reply := c.refreshBounds()
	reqCtx, phase := startRefreshPhase(ctx, send, reply)
	defer phase.stop()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.TokenURL, bytes.NewReader(body))
	if err != nil {
		debugf("OAuth refresh failed: %v", err)
		return RefreshOutcome{Error: ErrTransient}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		debugf("OAuth refresh failed: %v", err)
		return RefreshOutcome{Error: ErrTransient, Unanswered: phase.written()}
	}
	defer resp.Body.Close()
	// From here on the request was written: only a parsed success or a 4xx
	// is a verdict on the grant, anything else may have spent it.
	noVerdict := RefreshOutcome{Error: ErrTransient, Unanswered: true}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		debugf("OAuth refresh failed: HTTP %d redirect, not followed", resp.StatusCode)
		return noVerdict
	}
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		// Only the error code is logged: the body may echo request material.
		debugf("OAuth refresh failed: HTTP %d, error: %s", resp.StatusCode, oauthErrorCode(errBody))
		// Permanent only when the server rejected the grant: a 400/401/403 AND
		// an explicit marker in the body (case-sensitive substring).
		if (resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403) &&
			(bytes.Contains(errBody, []byte("invalid_grant")) || bytes.Contains(errBody, []byte("invalid_client"))) {
			return RefreshOutcome{Error: ErrInvalidGrant}
		}
		if resp.StatusCode >= 500 {
			return noVerdict
		}
		return RefreshOutcome{Error: ErrTransient}
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSuccessBody))
	if err != nil {
		debugf("OAuth refresh failed: token response cut off: %T", err)
		return noVerdict
	}
	respData, ok := decodeResponseMap(bytes.NewReader(raw))
	if !ok {
		debugf("OAuth refresh failed: undecodable token response")
		return noVerdict
	}
	accessToken, ok := respData["access_token"].(string)
	if !ok || accessToken == "" {
		debugf("OAuth refresh failed: access_token missing or not a non-empty string")
		return noVerdict
	}
	expiresIn, ok := numFloat(respData["expires_in"])
	if !ok || !(expiresIn > 0 && expiresIn < maxExpiresIn) {
		debugf("OAuth refresh failed: expires_in missing or out of range")
		return noVerdict
	}

	nowMs := time.Now().UTC().UnixMilli()
	oauth["accessToken"] = accessToken
	oauth["expiresAt"] = json.Number(strconv.FormatInt(nowMs+int64(expiresIn)*1000, 10))
	if rt, ok := respData["refresh_token"].(string); ok && rt != "" {
		oauth["refreshToken"] = rt
	}
	if scope, ok := respData["scope"].(string); ok && scope != "" {
		oauth["scopes"] = splitScopes(scope)
	}
	data["claudeAiOauth"] = oauth

	rotated, err := json.Marshal(data)
	if err != nil {
		debugf("OAuth refresh failed: re-encode: %v", err)
		return noVerdict
	}
	return RefreshOutcome{Credentials: string(rotated), TokenAccount: parseTokenAccount(respData)}
}

// Profile resolves an access token to its account identity, or nil on any
// failure (04§1.12). A 401 fails open with a WARNING; other errors log at DEBUG.
// Must not be called while any credential/config lock is held.
func (c *HTTPClient) Profile(ctx context.Context, accessToken string) *Identity {
	id, _ := c.ProfileAnswer(ctx, accessToken)
	return id
}

// ProfileAnswer implements ProfileProber: only an identity, a 401 or a 403
// is the endpoint's answer.
func (c *HTTPClient) ProfileAnswer(ctx context.Context, accessToken string) (*Identity, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.ProfileURL, nil)
	if err != nil {
		debugf("OAuth profile fetch failed: %v", err)
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		debugf("OAuth profile fetch failed: %v", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		if resp.StatusCode == 401 {
			warningf("OAuth profile returned 401 while resolving credential " +
				"ownership; proceeding without identity (pre-fix behavior).")
		} else {
			debugf("OAuth profile fetch failed: HTTP %d", resp.StatusCode)
		}
		return nil, resp.StatusCode == 401 || resp.StatusCode == 403
	}
	data, ok := decodeResponseMap(io.LimitReader(resp.Body, maxSuccessBody))
	if !ok {
		debugf("OAuth profile fetch failed: undecodable response")
		return nil, false
	}
	account, ok := data["account"].(map[string]any)
	if !ok {
		debugf("OAuth profile response missing account object")
		return nil, false
	}
	uuid, ok := account["uuid"].(string)
	if !ok || strings.TrimSpace(uuid) == "" {
		debugf("OAuth profile response missing account.uuid")
		return nil, false
	}
	id := &Identity{UUID: strings.TrimSpace(uuid)}
	if email, ok := account["email"].(string); ok {
		id.Email = email
	}
	if org, ok := data["organization"].(map[string]any); ok {
		if ou, ok := org["uuid"].(string); ok {
			id.OrgUUID = ou
		}
	}
	return id, true
}

// Usage fetches raw utilization data (04§1.15). Returns an *HTTPError for a
// non-2xx response (carrying Retry-After), a network/timeout error, or a
// bad-response error, so callers can classify. The beta header is usage-only;
// no Content-Type is sent.
func (c *HTTPClient) Usage(ctx context.Context, accessToken string) (map[string]any, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.UsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", OAuthBetaHeader)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, &HTTPError{
			Code:       resp.StatusCode,
			Body:       body,
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("usage: %w: HTTP %d redirect, not followed", errBadResponse, resp.StatusCode)
	}
	raw, decErr := decodeResponseMapErr(io.LimitReader(resp.Body, maxSuccessBody))
	if decErr != nil {
		return nil, fmt.Errorf("usage decode: %w: %w", errBadResponse, decErr)
	}
	return raw, nil
}

// oauthErrorCode extracts the OAuth "error" code from an error body for the
// log, limited to a short token of [A-Za-z0-9_.-]; "unknown" otherwise.
func oauthErrorCode(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return "unknown"
	}
	code, _ := m["error"].(string)
	if code == "" || len(code) > 64 {
		return "unknown"
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return "unknown"
		}
	}
	return code
}

// decodeResponseMap decodes a JSON response body into a map (json.Number),
// returning ok=false on any failure.
func decodeResponseMap(r io.Reader) (map[string]any, bool) {
	m, err := decodeResponseMapErr(r)
	return m, err == nil
}

func decodeResponseMapErr(r io.Reader) (map[string]any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// parseTokenAccount extracts the optional identity from a token-endpoint
// response (04§1.10). Requires a non-empty stripped string account.uuid; other
// fields normalize to "" when non-string.
func parseTokenAccount(resp map[string]any) *Identity {
	account, ok := resp["account"].(map[string]any)
	if !ok {
		return nil
	}
	uuid, ok := account["uuid"].(string)
	if !ok || strings.TrimSpace(uuid) == "" {
		return nil
	}
	id := &Identity{UUID: strings.TrimSpace(uuid)}
	if email, ok := account["email_address"].(string); ok {
		id.Email = email
	}
	if org, ok := resp["organization"].(map[string]any); ok {
		if ou, ok := org["uuid"].(string); ok {
			id.OrgUUID = ou
		}
	}
	return id
}

// FakeClient is an in-memory Client for tests and downstream fakes. Unset
// function fields fall back to benign defaults (Refresh -> transient,
// Profile -> nil, Usage -> empty success).
type FakeClient struct {
	RefreshFn func(ctx context.Context, creds string) RefreshOutcome
	ProfileFn func(ctx context.Context, accessToken string) *Identity
	UsageFn   func(ctx context.Context, accessToken string) (map[string]any, error)
}

// Refresh implements Client.
func (f *FakeClient) Refresh(ctx context.Context, creds string) RefreshOutcome {
	if f.RefreshFn != nil {
		return f.RefreshFn(ctx, creds)
	}
	return RefreshOutcome{Error: ErrTransient}
}

// Profile implements Client.
func (f *FakeClient) Profile(ctx context.Context, accessToken string) *Identity {
	if f.ProfileFn != nil {
		return f.ProfileFn(ctx, accessToken)
	}
	return nil
}

// ProfileAnswer implements ProfileProber: the fake's every reply is an answer.
func (f *FakeClient) ProfileAnswer(ctx context.Context, accessToken string) (*Identity, bool) {
	return f.Profile(ctx, accessToken), true
}

// Usage implements Client.
func (f *FakeClient) Usage(ctx context.Context, accessToken string) (map[string]any, error) {
	if f.UsageFn != nil {
		return f.UsageFn(ctx, accessToken)
	}
	return nil, nil
}

// compile-time assertions.
var (
	_ Client        = (*HTTPClient)(nil)
	_ Client        = (*FakeClient)(nil)
	_ ProfileProber = (*HTTPClient)(nil)
	_ ProfileProber = (*FakeClient)(nil)
)
