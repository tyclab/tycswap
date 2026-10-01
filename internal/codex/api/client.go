// client.go — the Codex network seam: the Client interface, its net/http
// implementation, a function-field fake, and the shared request plumbing.
//
// Ports the transport half of claude-swap PR #252 codex/oauth.py,
// codex/usage.py (_get_json) and the /backend-api/accounts call that
// codex/workspaces.py makes through usage.fetch_workspace_names. Shaped like
// internal/oauth/client.go: URLs are fields so tests can point them at an
// httptest server, the *http.Client is injectable, and every call is bounded by
// a context timeout (refresh 10 s, usage/accounts 5 s).
//
// None of these endpoints is documented by OpenAI, so nothing here returns a
// hard failure to a caller that cannot act on it: a refresh answers with an
// outcome kind, a usage fetch with a sentinel, and an accounts fetch with an
// error the caller treats as "leave the stored names alone". Logging is by
// status and category only — a request carries a refresh or access token, a
// response can echo request context, and these logs are what users paste into
// public issues, so neither tokens nor bodies are ever written to them.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/tyclab/tycswap/internal/logging"
)

// Timeouts per request class. The refresh gets the longer budget because a
// refresh that times out after the server rotated the token is the expensive
// failure; usage and accounts are read-only and cheap to retry.
const (
	refreshTimeout = 10 * time.Second
	fetchTimeout   = 5 * time.Second
)

// Body-size ceilings. Error bodies are only mined for an error code; success
// bodies are small JSON documents. Both are bounded so a misbehaving proxy
// cannot make tycswap buffer without limit.
const (
	maxErrorBody   = 1 << 16
	maxSuccessBody = 1 << 20
)

// Client is the network seam for Codex I/O. Implementations never panic and
// never return a failure the caller has to unwrap to stay safe: see the
// outcome/sentinel types on each method.
type Client interface {
	TryRefresh(ctx context.Context, payload map[string]any) RefreshOutcome
	FetchUsage(ctx context.Context, accessToken, accountID string) UsageFetch
	FetchAccounts(ctx context.Context, accessToken, accountID string) ([]Workspace, error)
}

// HTTPClient is the real Client backed by net/http. The URL fields default to
// the production endpoints and are overridable for tests.
type HTTPClient struct {
	Client      *http.Client
	TokenURL    string
	UsageURL    string
	AccountsURL string
}

// newSafeHTTPClient never follows a redirect, like internal/oauth's: a
// 307/308 from the token endpoint would re-POST the refresh token to whatever
// Location names, an http:// one included. The redirect response itself is
// returned, and every non-2xx status is a failure to the callers.
func newSafeHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var safeDefaultClient = newSafeHTTPClient()

// NewHTTPClient returns an HTTPClient pointed at the production endpoints.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		Client:      newSafeHTTPClient(),
		TokenURL:    OAuthTokenURL,
		UsageURL:    UsageURL,
		AccountsURL: AccountsURL,
	}
}

func (c *HTTPClient) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return safeDefaultClient
}

// StatusError is a non-2xx answer from a GET endpoint. It carries the status
// and the Retry-After header verbatim; the body is deliberately not kept.
type StatusError struct {
	Code       int
	RetryAfter string
}

func (e *StatusError) Error() string { return "http " + strconv.Itoa(e.Code) }

// errBadResponse marks a 2xx whose body was unreadable or not JSON.
var errBadResponse = errors.New("bad response")

// getJSON performs one authenticated GET against a chatgpt.com backend
// endpoint and decodes the JSON body (json.Number preserved, so an integer
// percentage stays an integer across the usage.json round trip). Both headers
// are required by the endpoint: the bearer token names the user, the
// ChatGPT-Account-Id names which of the user's workspaces is asking.
func (c *HTTPClient) getJSON(ctx context.Context, url, accessToken, accountID string) (any, error) {
	reqCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("ChatGPT-Account-Id", accountID)
	req.Header.Set("User-Agent", UserAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil, &StatusError{Code: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	data, err := decodeJSON(io.LimitReader(resp.Body, maxSuccessBody))
	if err != nil {
		return nil, errors.Join(errBadResponse, err)
	}
	return data, nil
}

// decodeJSON decodes one JSON value of any type with json.Number numbers.
// Python's json.loads accepts any top-level value, and so does this: a string
// or list body is a well-formed response that simply fails the shape checks.
func decodeJSON(r io.Reader) (any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// transportKind names a transport failure for logs without its message: Go's
// url.Error text embeds the request URL, and nothing that could carry request
// context belongs in a log line.
func transportKind(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "network"
}

// Log is the package logger seam, mirroring internal/oauth.Log. When nil,
// DEBUG lines are dropped.
var Log *logging.Logger

func debugf(format string, a ...any) {
	if Log != nil {
		Log.Debugf(format, a...)
	}
}

// FakeClient is an in-memory Client for tests and downstream fakes. Unset
// function fields fall back to benign failures (refresh -> transient, usage ->
// the "network" sentinel, accounts -> no workspaces), so a test that forgets a
// field sees an account that degrades rather than one that silently succeeds.
type FakeClient struct {
	RefreshFn  func(ctx context.Context, payload map[string]any) RefreshOutcome
	UsageFn    func(ctx context.Context, accessToken, accountID string) UsageFetch
	AccountsFn func(ctx context.Context, accessToken, accountID string) ([]Workspace, error)
}

// TryRefresh implements Client.
func (f *FakeClient) TryRefresh(ctx context.Context, payload map[string]any) RefreshOutcome {
	if f.RefreshFn != nil {
		return f.RefreshFn(ctx, payload)
	}
	return RefreshOutcome{Kind: KindTransient}
}

// FetchUsage implements Client.
func (f *FakeClient) FetchUsage(ctx context.Context, accessToken, accountID string) UsageFetch {
	if f.UsageFn != nil {
		return f.UsageFn(ctx, accessToken, accountID)
	}
	return UsageFetch{Sentinel: SentinelNetwork}
}

// FetchAccounts implements Client.
func (f *FakeClient) FetchAccounts(ctx context.Context, accessToken, accountID string) ([]Workspace, error) {
	if f.AccountsFn != nil {
		return f.AccountsFn(ctx, accessToken, accountID)
	}
	return nil, nil
}

// compile-time assertions.
var (
	_ Client = (*HTTPClient)(nil)
	_ Client = (*FakeClient)(nil)
)

// truthy is Python truthiness for decoded-JSON values: the checks this port
// mirrors are `if x`/`not x`, not type tests, and a zero number, empty string,
// empty container or JSON null must all read as false.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case float64:
		return x != 0
	case float32:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case int32:
		return x != 0
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return true
}

// number reports whether v is a JSON number (Python isinstance(v, (int,
// float)) and not bool), returning its float64 value.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	}
	return 0, false
}
