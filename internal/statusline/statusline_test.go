package statusline

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/usage"
)

func fptr(f float64) *float64 { return &f }

var now = time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)

// decode round-trips doc through the published bytes, numbers kept exact.
func decode(t *testing.T, doc Document) map[string]any {
	t.Helper()
	b, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("published bytes do not parse: %v\n%s", err, b)
	}
	return m
}

// The file has exactly the documented top-level keys.
func TestBuildTopLevel(t *testing.T) {
	m := decode(t, Build(Input{ProducerVersion: "0.7.7", Now: now.Add(900 * time.Millisecond)}))
	if len(m) != 5 || m["schemaVersion"] != json.Number("1") || m["producer"] != "tycswap" ||
		m["producerVersion"] != "0.7.7" || m["writtenAt"] != json.Number("1784278800") {
		t.Errorf("document = %v", m)
	}
	if a, ok := m["accounts"].(map[string]any); !ok || len(a) != 0 {
		t.Errorf("accounts = %v, want {}", m["accounts"])
	}
}

// Keys are email|organizationUuid: one email in two organizations is two
// entries, and a null organization (a personal login, an API-key account) is
// "email|". Labels are alias > organization name > email, control characters
// stripped; a duplicate identity publishes the lower slot.
func TestBuildKeysAndLabels(t *testing.T) {
	doc := Build(Input{Now: now, Records: []Record{
		{Slot: 2, Email: "a@x.com", OrganizationUUID: "org-b", OrgName: "Org B"},
		{Slot: 1, Email: "a@x.com", OrganizationUUID: "org-a", OrgName: "Org A", Alias: "work\x1b[31m"},
		{Slot: 3, Email: "api-key-3@token.local"},
		{Slot: 4, Email: "a@x.com", OrganizationUUID: "org-a", Alias: "dup"},
		{Slot: 5},
	}})
	want := map[string]Account{
		"a@x.com|org-a":          {Slot: 1, Label: "work[31m"},
		"a@x.com|org-b":          {Slot: 2, Label: "Org B"},
		"api-key-3@token.local|": {Slot: 3, Label: "api-key-3@token.local"},
	}
	if len(doc.Accounts) != len(want) {
		t.Fatalf("accounts = %+v", doc.Accounts)
	}
	for k, w := range want {
		if got, ok := doc.Accounts[k]; !ok || got != w {
			t.Errorf("%s = %+v (present %v), want %+v", k, got, ok, w)
		}
	}
}

// Every time is an integer epoch: fetchedAt floored, a fractional ISO reset
// truncated, pollIntervalS rounded. Windows without a numeric pct are left out.
func TestBuildUsage(t *testing.T) {
	lg := map[string]any{
		"five_hour": map[string]any{"pct": json.Number("6"), "resets_at": "2026-07-17T12:30:00.123456+00:00"},
		"seven_day": map[string]any{"pct": 88.5, "resets_at": "2026-07-20T00:00:00Z"},
		"spend":     map[string]any{"pct": 10.0},
		"scoped": []any{
			map[string]any{"name": "Fable", "pct": 12.0, "resets_at": "2026-07-19T08:00:00Z"},
			map[string]any{"name": "Bad", "pct": "n/a"},
		},
	}
	doc := Build(Input{
		Now:     now,
		Records: []Record{{Slot: 1, Email: "a@x.com"}, {Slot: 2, Email: "b@x.com"}, {Slot: 3, Email: "c@x.com"}},
		Usage: map[int]usage.UsageEntry{
			1: {LastGood: lg, FetchedAt: fptr(1784278799.987), PollIntervalS: fptr(299.6)},
			2: {LastGood: map[string]any{"five_hour": map[string]any{"pct": 3.0}, "seven_day": map[string]any{"pct": math.Inf(1)}}, FetchedAt: fptr(100)},
			3: {LastError: "http-429"},
		},
	})
	accts := decode(t, doc)["accounts"].(map[string]any)
	u := accts["a@x.com|"].(map[string]any)["usage"].(map[string]any)
	want := `{"fetchedAt":1784278799,"fiveHour":{"pct":6,"resetsAt":1784291400},"pollIntervalS":300,` +
		`"scoped":[{"name":"Fable","pct":12,"resetsAt":1784448000}],"sevenDay":{"pct":88.5,"resetsAt":1784505600}}`
	if b, _ := json.Marshal(u); string(b) != want {
		t.Errorf("usage =\n %s\nwant\n %s", b, want)
	}
	u2 := accts["b@x.com|"].(map[string]any)["usage"].(map[string]any)
	want2 := `{"fetchedAt":100,"fiveHour":{"pct":3,"resetsAt":null},"pollIntervalS":` +
		fmt.Sprint(DefaultPollIntervalS) + `,"scoped":[],"sevenDay":null}`
	if b, _ := json.Marshal(u2); string(b) != want2 {
		t.Errorf("usage without plan or reset =\n %s\nwant\n %s", b, want2)
	}
	if c := accts["c@x.com|"].(map[string]any); c["usage"] != nil || len(c) != 3 {
		t.Errorf("never measured = %v, want usage null and no other keys", c)
	}
}
