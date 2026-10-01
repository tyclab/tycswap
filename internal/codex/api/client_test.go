// client_test.go — the FakeClient defaults, the shared truthiness helper, and
// the context timeout on every request.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFakeClient_DefaultsDegrade(t *testing.T) {
	f := &FakeClient{}
	ctx := context.Background()
	if got := f.TryRefresh(ctx, nil).Kind; got != KindTransient {
		t.Errorf("default refresh kind = %q", got)
	}
	if got := f.FetchUsage(ctx, "at", "acct"); got.Sentinel != SentinelNetwork || got.Usage != nil {
		t.Errorf("default usage = %+v", got)
	}
	if ws, err := f.FetchAccounts(ctx, "at", "acct"); ws != nil || err != nil {
		t.Errorf("default accounts = %v, %v", ws, err)
	}
}

func TestFakeClient_FunctionFieldsAreCalled(t *testing.T) {
	f := &FakeClient{
		RefreshFn: func(context.Context, map[string]any) RefreshOutcome { return RefreshOutcome{Kind: KindInvalidGrant} },
		UsageFn: func(_ context.Context, tok, acc string) UsageFetch {
			return UsageFetch{Sentinel: tok + "/" + acc}
		},
		AccountsFn: func(context.Context, string, string) ([]Workspace, error) {
			return []Workspace{{AccountID: "a", Name: "A"}}, nil
		},
	}
	ctx := context.Background()
	if f.TryRefresh(ctx, nil).Kind != KindInvalidGrant {
		t.Error("RefreshFn not used")
	}
	if f.FetchUsage(ctx, "t", "a").Sentinel != "t/a" {
		t.Error("UsageFn not used")
	}
	if ws, _ := f.FetchAccounts(ctx, "", ""); len(ws) != 1 {
		t.Error("AccountsFn not used")
	}
}

func TestTruthy(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{nil, false}, {false, false}, {true, true}, {"", false}, {"x", true},
		{json.Number("0"), false}, {json.Number("0.0"), false}, {json.Number("3"), true},
		{0.0, false}, {1, true}, {map[string]any{}, false}, {map[string]any{"a": 1}, true},
		{[]any{}, false}, {[]any{1}, true},
	}
	for _, tc := range cases {
		if got := truthy(tc.in); got != tc.want {
			t.Errorf("truthy(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A caller's context bounds every request, and a server that never answers
// degrades to the transient / network verdicts rather than hanging.
func TestRequestsHonourTheCallerContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block); srv.Close() })
	c := NewHTTPClient()
	c.TokenURL, c.UsageURL, c.AccountsURL = srv.URL, srv.URL, srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := c.TryRefresh(ctx, defaultAuth(t)).Kind; got != KindTransient {
		t.Errorf("refresh kind = %q", got)
	}
	if got := c.FetchUsage(ctx, "at", "acct").Sentinel; got != SentinelNetwork {
		t.Errorf("usage sentinel = %q", got)
	}
	if _, err := c.FetchAccounts(ctx, "at", "acct"); err == nil {
		t.Error("accounts: want an error")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("requests ignored the context deadline: %v", time.Since(start))
	}
}

// A 307/308 from any endpoint is not followed: the token endpoint's answer
// would otherwise be re-POSTed, refresh token included, to whatever Location
// names. The redirect is a failed request; the target sees nothing. The
// default client, used when none is set, behaves the same.
func TestRedirectsAreNotFollowed(t *testing.T) {
	target := newServer(t, 200, mustJSON(t, map[string]any{"access_token": "leaked"}), nil)
	for _, status := range []int{307, 308, 302} {
		redirect := newServer(t, status, "", map[string]string{"Location": target.srv.URL + "/elsewhere"})
		for _, c := range []*HTTPClient{clientFor(redirect), func() *HTTPClient {
			c := clientFor(redirect)
			c.Client = nil
			return c
		}()} {
			ctx := context.Background()
			if got := c.TryRefresh(ctx, defaultAuth(t)).Kind; got != KindTransient {
				t.Errorf("%d: refresh kind = %q, want transient", status, got)
			}
			if got := c.FetchUsage(ctx, "at", "acct"); got.Usage != nil || got.Sentinel == "" {
				t.Errorf("%d: usage = %+v, want a sentinel and no data", status, got)
			}
			if ws, err := c.FetchAccounts(ctx, "at", "acct"); err == nil || ws != nil {
				t.Errorf("%d: accounts = %v, %v, want an error", status, ws, err)
			}
		}
		if redirect.hits() != 6 {
			t.Errorf("%d: redirecting server saw %d requests, want 6", status, redirect.hits())
		}
	}
	if target.hits() != 0 {
		t.Errorf("redirect target saw %d requests; a refresh token was re-posted", target.hits())
	}
}
