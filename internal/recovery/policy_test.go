package recovery

import (
	"math"
	"testing"
	"time"
)

var epoch = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func account(id string, waits ...time.Duration) Account {
	a := Account{ID: id, Identity: "identity-" + id, Eligible: true, OwnershipKnown: true, Available: true, QuotaKnown: true, FetchedAt: epoch}
	for i, w := range waits {
		a.Windows = append(a.Windows, Window{Name: string(rune('a' + i)), Used: 100, ResetAt: epoch.Add(w)})
	}
	if len(waits) == 0 {
		a.Windows = []Window{{Name: "shared", Used: 20}}
	}
	return a
}

func input(accounts ...Account) Input {
	return Input{Event: Event{AccountID: "a", AccountIdentity: "identity-a", SessionID: "s", IncidentID: "turn", Provider: "claude", Group: "opus", Model: "opus", Stopped: true, Error: "rate_limit"}, CurrentAccount: "a", Accounts: accounts, DestinationUsable: true, Now: epoch}
}

func TestLatestBlockingResetThenEarliestEligibleAccount(t *testing.T) {
	in := input(account("a", 20*time.Minute, 90*time.Minute), account("b", 60*time.Minute, 40*time.Minute))
	d := Evaluate(in)
	if d.Action != "offer" || !d.ReadyAt.Equal(epoch.Add(time.Hour)) {
		t.Fatalf("decision %#v", d)
	}
	in.Accounts[1].Windows[0].ResetAt = epoch.Add(20 * time.Minute)
	in.Accounts[1].Windows[1].ResetAt = epoch.Add(15 * time.Minute)
	if d := Evaluate(in); d.Action != "wait" || !d.ReadyAt.Equal(epoch.Add(20*time.Minute)) {
		t.Fatalf("decision %#v", d)
	}
	in.Wait = 15 * time.Minute
	if d := Evaluate(in); d.Action != "offer" {
		t.Fatalf("15-minute option: %#v", d)
	}
}

func TestRotationBeforeHandoverAndModelSpecificWindows(t *testing.T) {
	a := account("a", time.Hour)
	b := account("b")
	b.Windows = append(b.Windows, Window{Name: "fable", Model: "fable", Used: 100, ResetAt: epoch.Add(2 * time.Hour)})
	d := Evaluate(input(a, b))
	if d.Action != "rotate" || d.Account != "b" {
		t.Fatalf("Fable must not block Opus: %#v", d)
	}
	in := input(a, b)
	in.Event.Model = "fable"
	in.Event.Group = "fable"
	if d := Evaluate(in); d.Action != "offer" {
		t.Fatalf("Fable blocked: %#v", d)
	}
}

func TestUnknownEvidenceNeverOffers(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
	}{
		{"stale", func(in *Input) { in.Accounts[1].FetchedAt = epoch.Add(-4 * time.Minute) }},
		{"future observation", func(in *Input) { in.Accounts[1].FetchedAt = epoch.Add(time.Minute) }},
		{"missing quota", func(in *Input) { in.Accounts[1].QuotaKnown = false }},
		{"missing windows", func(in *Input) { in.Accounts[1].Windows = nil }},
		{"past reset", func(in *Input) { in.Accounts[1].Windows[0].ResetAt = epoch }},
		{"missing reset", func(in *Input) { in.Accounts[1].Windows[0].ResetAt = time.Time{} }},
		{"unknown ownership", func(in *Input) { in.Accounts[1].OwnershipKnown = false }},
		{"owned elsewhere", func(in *Input) { in.Accounts[1].Available = false }},
		{"unknown model", func(in *Input) { in.Event.Model = "mystery" }},
		{"wrong group", func(in *Input) { in.Event.Model = "fable" }},
		{"invalid percent", func(in *Input) { in.Accounts[1].Windows[0].Used = math.NaN() }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			in := input(account("a", time.Hour), account("b", time.Hour))
			tt.mutate(&in)
			if d := Evaluate(in); d.Action != "unknown" {
				t.Fatalf("%#v", d)
			}
		})
	}
}

func TestOnlyStoppedCorroboratedLimitsTrigger(t *testing.T) {
	for _, errorType := range []string{"", "429", "overloaded", "network", "authentication_failed", "manual_stop", "crash", "model_capacity"} {
		in := input(account("a", time.Hour))
		in.Event.Error = errorType
		if d := Evaluate(in); d.Action != "ignore" {
			t.Fatalf("%s: %#v", errorType, d)
		}
	}
	in := input(account("a"))
	if d := Evaluate(in); d.Action != "ignore" {
		t.Fatalf("generic rate limit without quota: %#v", d)
	}
	in = input(account("a", time.Hour))
	in.Event.Stopped = false
	if d := Evaluate(in); d.Action != "ignore" {
		t.Fatalf("%#v", d)
	}
	in = input(account("a", time.Hour))
	in.DestinationUsable = false
	if d := Evaluate(in); d.Action != "wait" {
		t.Fatalf("blocked destination: %#v", d)
	}
}

func TestOtherGroupModelsUseTheirOwnScopedQuota(t *testing.T) {
	a := account("a", time.Hour)
	b := account("b")
	b.Windows = append(b.Windows, Window{Name: "Sonnet", Model: "sonnet", Used: 100, ResetAt: epoch.Add(time.Hour)})
	in := input(a, b)
	if d := Evaluate(in); d.Action != "rotate" {
		t.Fatalf("Sonnet quota blocked Opus %#v", d)
	}
	in.Event.Model = "sonnet"
	if d := Evaluate(in); d.Action != "offer" {
		t.Fatalf("Sonnet quota not applied %#v", d)
	}
}

func TestStoppedLimitMustStayBoundToOriginalAccountIdentity(t *testing.T) {
	in := input(account("a"), account("b", time.Hour))
	in.CurrentAccount = "b"
	if d := Evaluate(in); d.Action != "unknown" {
		t.Fatalf("A error corroborated against B %#v", d)
	}
	in = input(account("a", time.Hour))
	in.Accounts[0].Identity = "replaced-slot-identity"
	if d := Evaluate(in); d.Action != "unknown" {
		t.Fatalf("replaced account slot accepted %#v", d)
	}
	in = input(account("a", time.Hour))
	in.Event.AccountID = ""
	in.Event.AccountIdentity = ""
	if d := Evaluate(in); d.Action != "unknown" {
		t.Fatalf("unbound incident accepted %#v", d)
	}
}
