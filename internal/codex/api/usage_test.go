// usage_test.go — mapping the ChatGPT usage API onto tycswap's usage map
// (claude-swap PR #252 tests/test_codex_usage.py), driven by httptest servers.

package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/oauth"
)

// rawJSON is the live endpoint's real wire shape, captured 2026-08-16. NOT
// codex-auth's normalized last_usage shape — assuming those were the same cost
// a bug only the live smoke test caught.
const rawJSON = `{
  "user_id": "user-a", "account_id": "acct-a", "email": "a@example.com",
  "plan_type": "pro",
  "rate_limit": {
    "allowed": true, "limit_reached": false,
    "primary_window": {"used_percent": 42, "limit_window_seconds": 18000, "reset_after_seconds": 900, "reset_at": 1800000000},
    "secondary_window": {"used_percent": 7, "limit_window_seconds": 604800, "reset_after_seconds": 90000, "reset_at": 1800600000}
  },
  "credits": {"has_credits": true, "unlimited": false, "overage_limit_reached": false, "balance": "12.50"}
}`

// livePlusJSON is two live Plus accounts on 2026-08-16: primary_window was a
// WEEK and secondary_window was null.
const livePlusJSON = `{
  "email": "a@example.com", "plan_type": "plus",
  "rate_limit": {
    "allowed": true, "limit_reached": false,
    "primary_window": {"used_percent": 98, "limit_window_seconds": 604800, "reset_after_seconds": 341770, "reset_at": 1800600000},
    "secondary_window": null
  },
  "credits": {"has_credits": false, "unlimited": false, "balance": null}
}`

// fixedNow is well before both fixture resets, so countdowns are positive.
var fixedNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	v, err := decodeJSON(strings.NewReader(s))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return v.(map[string]any)
}

// raw is the RAW fixture with rate_limit windows replaced by the given ones
// (a nil value means JSON null).
func raw(t *testing.T, windows map[string]any) map[string]any {
	t.Helper()
	d := decode(t, rawJSON)
	rl := d["rate_limit"].(map[string]any)
	for k, v := range windows {
		rl[k] = v
	}
	return d
}

func build(data any) map[string]any { return buildUsageResult(data, fixedNow) }

func win(t *testing.T, out map[string]any, slot string) map[string]any {
	t.Helper()
	w, ok := out[slot].(map[string]any)
	if !ok {
		t.Fatalf("%s absent from %v", slot, out)
	}
	return w
}

func pct(t *testing.T, w map[string]any) float64 {
	t.Helper()
	f, ok := number(w["pct"])
	if !ok {
		t.Fatalf("pct %v (%T) is not a number", w["pct"], w["pct"])
	}
	return f
}

// tycswap's renderers, pace and autoswitch all read five_hour/seven_day; the
// mapping is what makes every Claude-side consumer work unchanged.
func TestPrimaryWindowMapsToFiveHour(t *testing.T) {
	w := win(t, build(decode(t, rawJSON)), "five_hour")
	if pct(t, w) != 42 {
		t.Errorf("pct = %v, want 42", w["pct"])
	}
	if ra, _ := w["resets_at"].(string); !strings.HasPrefix(ra, "2027-01-15") {
		t.Errorf("resets_at = %v", w["resets_at"])
	}
}

func TestSecondaryWindowMapsToSevenDay(t *testing.T) {
	if got := pct(t, win(t, build(decode(t, rawJSON)), "seven_day")); got != 7 {
		t.Errorf("pct = %v, want 7", got)
	}
}

// The integer percentage passes through undecorated, as on the Claude side.
func TestPctPassesThroughAsDecoded(t *testing.T) {
	w := win(t, build(decode(t, rawJSON)), "five_hour")
	if w["pct"] != json.Number("42") {
		t.Errorf("pct = %#v, want json.Number(\"42\")", w["pct"])
	}
}

func TestWindowsCarryCountdownAndClockLikeTheClaudeSide(t *testing.T) {
	w := win(t, build(decode(t, rawJSON)), "five_hour")
	wantCD, wantCK, _ := oauth.FormatReset(w["resets_at"].(string), fixedNow)
	if w["countdown"] != wantCD || w["clock"] != wantCK {
		t.Errorf("countdown/clock = %v/%v, want %v/%v", w["countdown"], w["clock"], wantCD, wantCK)
	}
	if wantCD == "" || wantCK == "" {
		t.Errorf("empty countdown/clock: %v", w)
	}
}

// The API reports epoch seconds; pace parses ISO strings. A raw epoch would
// silently disable pace for every Codex row.
func TestEpochResetsAreConvertedToISO(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{json.Number("1800000000"), "2027-01-15T08:00:00+00:00"},
		{1800000000, "2027-01-15T08:00:00+00:00"},
		{1800000000.5, "2027-01-15T08:00:00.500000+00:00"},
		{json.Number("0"), ""},
		{-5, ""},
		{true, ""},
		{"1800000000", ""},
		{nil, ""},
		{1e300, ""},
	}
	for _, tc := range cases {
		if got := isoFromEpoch(tc.in); got != tc.want {
			t.Errorf("isoFromEpoch(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	w := win(t, build(decode(t, rawJSON)), "five_hour")
	if _, err := time.Parse(time.RFC3339, w["resets_at"].(string)); err != nil {
		t.Errorf("resets_at not ISO-8601: %v", err)
	}
}

// The real contract this mapping exists to satisfy: the Claude-side consumers
// read it. tycswap has no pace module, so its stand-ins are the typed projection
// (weekly renewal is what pace keys on) and the JSON renderer.
func TestTheMappedShapeIsReadableByTheClaudeSideConsumers(t *testing.T) {
	for name, fixture := range map[string]string{"two windows": rawJSON, "live plus": livePlusJSON} {
		out := build(decode(t, fixture))
		u := oauth.NewUsage(out)
		if u == nil || u.SevenDay == nil {
			t.Fatalf("%s: NewUsage lost the weekly window: %+v", name, u)
		}
		ts := oauth.RenewalTS(u, nil)
		if ts == nil || *ts != 1_800_600_000 {
			t.Errorf("%s: RenewalTS = %v, want 1800600000", name, ts)
		}
		js := jsonout.UsageToJSON(out)
		if js == nil {
			t.Errorf("%s: UsageToJSON rejected the map", name)
		}
	}
}

func TestWindowEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		windows map[string]any
		check   func(t *testing.T, out map[string]any)
	}{
		{"missing secondary is omitted, not zeroed", map[string]any{"secondary_window": nil}, func(t *testing.T, out map[string]any) {
			if _, ok := out["seven_day"]; ok {
				t.Errorf("seven_day present: %v", out["seven_day"])
			}
		}},
		{"a window without a percentage is dropped", map[string]any{"primary_window": map[string]any{"limit_window_seconds": 18000, "reset_at": 1}}, func(t *testing.T, out map[string]any) {
			if _, ok := out["five_hour"]; ok {
				t.Errorf("five_hour present: %v", out["five_hour"])
			}
		}},
		{"a boolean percentage is not a number", map[string]any{"primary_window": map[string]any{"used_percent": true}}, func(t *testing.T, out map[string]any) {
			if _, ok := out["five_hour"]; ok {
				t.Errorf("five_hour present: %v", out["five_hour"])
			}
		}},
		{"a window without a reset still reports its percentage", map[string]any{"primary_window": map[string]any{"used_percent": 12}}, func(t *testing.T, out map[string]any) {
			if w := win(t, out, "five_hour"); !reflect.DeepEqual(w, map[string]any{"pct": 12}) {
				t.Errorf("five_hour = %v, want {pct: 12}", w)
			}
		}},
		{"a five-hour primary is five-hourly", map[string]any{"primary_window": map[string]any{"used_percent": 30, "limit_window_seconds": 18000, "reset_at": 1800000000}, "secondary_window": nil}, func(t *testing.T, out map[string]any) {
			if pct(t, win(t, out, "five_hour")) != 30 {
				t.Errorf("five_hour = %v", out["five_hour"])
			}
			if _, ok := out["seven_day"]; ok {
				t.Errorf("seven_day present")
			}
		}},
		{"no declared length falls back to position", map[string]any{"primary_window": map[string]any{"used_percent": 11, "reset_at": 1800000000}, "secondary_window": map[string]any{"used_percent": 22, "reset_at": 1800600000}}, func(t *testing.T, out map[string]any) {
			if pct(t, win(t, out, "five_hour")) != 11 || pct(t, win(t, out, "seven_day")) != 22 {
				t.Errorf("out = %v", out)
			}
		}},
		{"two windows of one class: the first wins", map[string]any{"primary_window": map[string]any{"used_percent": 60, "limit_window_seconds": 604800}, "secondary_window": map[string]any{"used_percent": 5, "limit_window_seconds": 604800}}, func(t *testing.T, out map[string]any) {
			if pct(t, win(t, out, "seven_day")) != 60 {
				t.Errorf("seven_day = %v, want primary's 60", out["seven_day"])
			}
			if _, ok := out["five_hour"]; ok {
				t.Errorf("five_hour invented: %v", out["five_hour"])
			}
		}},
		{"the weekly boundary is inclusive", map[string]any{"primary_window": map[string]any{"used_percent": 1, "limit_window_seconds": WeeklyWindowMinS}, "secondary_window": nil}, func(t *testing.T, out map[string]any) {
			win(t, out, "seven_day")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, build(raw(t, tc.windows))) })
	}
}

// The regression this whole classification exists for: mislabelling a weekly
// primary five_hour would silently disable pace forever.
func TestAWeeklyPrimaryWindowIsClassifiedAsWeekly(t *testing.T) {
	out := build(decode(t, livePlusJSON))
	if pct(t, win(t, out, "seven_day")) != 98 {
		t.Errorf("seven_day = %v", out["seven_day"])
	}
	if _, ok := out["five_hour"]; ok {
		t.Errorf("five_hour present: %v", out["five_hour"])
	}
	if _, ok := out["spend"]; ok {
		t.Errorf("spend present without credits: %v", out["spend"])
	}
	if out["plan"] != "plus" {
		t.Errorf("plan = %v", out["plan"])
	}
}

func TestBothWindowsAreClassifiedIndependently(t *testing.T) {
	out := build(decode(t, rawJSON))
	if pct(t, win(t, out, "five_hour")) != 42 || pct(t, win(t, out, "seven_day")) != 7 {
		t.Errorf("out = %v", out)
	}
}

func TestPlanTypeIsNormalizedToV4Semantics(t *testing.T) {
	d := decode(t, rawJSON)
	d["plan_type"] = "team"
	if got := build(d)["plan"]; got != "business" {
		t.Errorf("plan = %v, want business", got)
	}
}

func TestCreditsBecomeASpendEntry(t *testing.T) {
	cases := []struct {
		name    string
		credits any
		want    any // nil means no spend key
	}{
		{"priced credits", map[string]any{"has_credits": true, "unlimited": false, "balance": "12.50"}, map[string]any{"unlimited": false, "balance": "12.50"}},
		{"unlimited credits are marked not priced", map[string]any{"has_credits": true, "unlimited": true, "balance": nil}, map[string]any{"unlimited": true, "balance": nil}},
		{"no credits, no spend", map[string]any{"has_credits": false}, nil},
		{"credits not an object", "lots", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := decode(t, rawJSON)
			d["credits"] = tc.credits
			got, ok := build(d)["spend"]
			if tc.want == nil {
				if ok {
					t.Errorf("spend = %v, want absent", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("spend = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Every consumer needs a window; a plan-only map renders as a blank row with
// no explanation, which is exactly how the first live run failed.
func TestAResponseWithNoWindowIsNotUsableUsage(t *testing.T) {
	cases := map[string]any{
		"empty object":  map[string]any{},
		"nil":           nil,
		"list":          []any{1, 2},
		"plan only":     map[string]any{"plan_type": "pro"},
		"null windows":  raw(t, map[string]any{"primary_window": nil, "secondary_window": nil}),
		"string body":   "junk",
		"rate_limit []": map[string]any{"rate_limit": []any{}},
	}
	for name, in := range cases {
		if got := BuildUsageResult(in); got != nil {
			t.Errorf("%s: BuildUsageResult = %v, want nil", name, got)
		}
	}
}

func TestFetch_SendsBothRequiredHeaders(t *testing.T) {
	s := newServer(t, 200, rawJSON, nil)
	clientFor(s).FetchUsage(context.Background(), "at-1", "acct-1")
	req := s.last(t)
	if req.Method != "GET" || req.Path != "/backend-api/wham/usage" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer at-1" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-1" {
		t.Errorf("ChatGPT-Account-Id = %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != UserAgent {
		t.Errorf("User-Agent = %q", got)
	}
	if NewHTTPClient().UsageURL != UsageURL {
		t.Errorf("production UsageURL = %q", NewHTTPClient().UsageURL)
	}
}

func TestFetch_ReturnsTheMappedUsage(t *testing.T) {
	s := newServer(t, 200, rawJSON, nil)
	res := clientFor(s).FetchUsage(context.Background(), "at-1", "acct-1")
	if res.Sentinel != "" {
		t.Fatalf("sentinel = %q", res.Sentinel)
	}
	if pct(t, win(t, res.Usage, "five_hour")) != 42 {
		t.Errorf("usage = %v", res.Usage)
	}
}

// Sentinels, not errors: codex-auth renders the status in the usage column,
// and matching that keeps a broken account legible instead of blank.
func TestFetch_Sentinels(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		header     map[string]string
		token, acc string
		sentinel   string
		retryAfter *float64
	}{
		{name: "http status", status: 401, body: `{}`, token: "at-1", acc: "acct-1", sentinel: "http 401"},
		{name: "retry-after parsed", status: 429, body: `{}`, header: map[string]string{"Retry-After": "120"}, token: "at", acc: "acct", sentinel: "http 429", retryAfter: ptr(120)},
		{name: "http-date retry-after ignored", status: 429, body: `{}`, header: map[string]string{"Retry-After": "Wed, 21 Oct 2026 07:28:00 GMT"}, token: "at", acc: "acct", sentinel: "http 429"},
		{name: "negative retry-after ignored", status: 429, body: `{}`, header: map[string]string{"Retry-After": "-3"}, token: "at", acc: "acct", sentinel: "http 429"},
		{name: "no retry-after", status: 500, body: `{}`, token: "at", acc: "acct", sentinel: "http 500"},
		{name: "unparseable body", status: 200, body: `junk`, token: "at", acc: "acct", sentinel: SentinelBadResponse},
		{name: "json string body", status: 200, body: `"junk"`, token: "at", acc: "acct", sentinel: SentinelBadResponse},
		{name: "windowless response", status: 200, body: `{"plan_type":"pro"}`, token: "at", acc: "acct", sentinel: SentinelBadResponse},
		{name: "no account id", status: 200, body: rawJSON, token: "at-1", acc: "", sentinel: "MissingAuth"},
		{name: "no access token", status: 200, body: rawJSON, token: "", acc: "acct-1", sentinel: "MissingAuth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, tc.status, tc.body, tc.header)
			res := clientFor(s).FetchUsage(context.Background(), tc.token, tc.acc)
			if res.Sentinel != tc.sentinel || res.Usage != nil {
				t.Errorf("result = %+v, want sentinel %q", res, tc.sentinel)
			}
			if !reflect.DeepEqual(res.RetryAfterS, tc.retryAfter) {
				t.Errorf("retry-after = %v, want %v", deref(res.RetryAfterS), deref(tc.retryAfter))
			}
			if tc.sentinel == "MissingAuth" && s.hits() != 0 {
				t.Errorf("MissingAuth made %d requests", s.hits())
			}
		})
	}
}

func TestFetch_ANetworkFailureIsReportedNotRaised(t *testing.T) {
	if got := deadClient(t).FetchUsage(context.Background(), "at", "acct").Sentinel; got != SentinelNetwork {
		t.Errorf("sentinel = %q, want network", got)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	cases := map[string]*float64{
		"120": ptr(120), " 7 ": ptr(7), "1.5": ptr(1.5), "0": ptr(0),
		"": nil, "-1": nil, "NaN": nil, "soon": nil, "Wed, 21 Oct 2026 07:28:00 GMT": nil,
	}
	for in, want := range cases {
		if got := retryAfterSeconds(in); !reflect.DeepEqual(got, want) {
			t.Errorf("retryAfterSeconds(%q) = %v, want %v", in, deref(got), deref(want))
		}
	}
}

func ptr(f float64) *float64 { return &f }

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
