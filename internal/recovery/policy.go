package recovery

import (
	"math"
	"strings"
	"time"
)

const DefaultWait = 30 * time.Minute
const FreshQuota = 3 * time.Minute

type Window struct {
	Name    string    `json:"name"`
	Model   string    `json:"model,omitempty"`
	Used    float64   `json:"used"`
	ResetAt time.Time `json:"reset_at,omitempty"`
}

type Account struct {
	ID             string    `json:"id"`
	Identity       string    `json:"identity"`
	Eligible       bool      `json:"eligible"`
	OwnershipKnown bool      `json:"ownership_known"`
	Available      bool      `json:"available"`
	QuotaKnown     bool      `json:"quota_known"`
	FetchedAt      time.Time `json:"fetched_at"`
	Windows        []Window  `json:"windows"`
}

type Input struct {
	Event             Event         `json:"event"`
	CurrentAccount    string        `json:"current_account"`
	Accounts          []Account     `json:"accounts"`
	DestinationUsable bool          `json:"destination_usable"`
	Now               time.Time     `json:"now"`
	Wait              time.Duration `json:"wait"`
}

type Decision struct {
	Action       string    `json:"action"`
	Reason       string    `json:"reason"`
	Account      string    `json:"account,omitempty"`
	ReadyAt      time.Time `json:"ready_at,omitempty"`
	NewlyOffered bool      `json:"newly_offered,omitempty"`
}

func ModelClass(model string) string {
	m := strings.ToLower(model)
	if m == "fable" || strings.HasPrefix(m, "claude-fable-") {
		return "fable"
	}
	if m == "opus" || strings.HasPrefix(m, "claude-opus-") || m == "sonnet" || strings.HasPrefix(m, "claude-sonnet-") || m == "haiku" || strings.HasPrefix(m, "claude-haiku-") {
		return "opus"
	}
	if strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex-") || m == "codex" {
		return "codex"
	}
	return ""
}

func ModelFamily(model string) string {
	m := strings.ToLower(model)
	for _, family := range []string{"fable", "opus", "sonnet", "haiku"} {
		if m == family || strings.HasPrefix(m, "claude-"+family+"-") {
			return family
		}
	}
	if ModelClass(model) == "codex" {
		return "codex"
	}
	return ""
}

func accountReady(a Account, model string, now time.Time) (time.Time, bool, bool) {
	if !a.OwnershipKnown || !a.Available || !a.QuotaKnown || a.FetchedAt.IsZero() || a.FetchedAt.After(now) || now.Sub(a.FetchedAt) > FreshQuota {
		return time.Time{}, false, false
	}
	ready := now
	blocked := false
	relevant := false
	for _, w := range a.Windows {
		if w.Model != "" && w.Model != model {
			continue
		}
		relevant = true
		if math.IsNaN(w.Used) || math.IsInf(w.Used, 0) || w.Used < 0 {
			return time.Time{}, false, false
		}
		if w.Used < 100 {
			continue
		}
		blocked = true
		if w.ResetAt.IsZero() || !w.ResetAt.After(now) {
			return time.Time{}, false, true
		}
		if w.ResetAt.After(ready) {
			ready = w.ResetAt
		}
	}
	return ready, relevant, blocked
}

func Evaluate(in Input) Decision {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	if in.Wait == 0 {
		in.Wait = DefaultWait
	}
	e := in.Event
	if !e.Stopped || (e.Error != "rate_limit" && e.Error != "usage_limit") {
		return Decision{Action: "ignore", Reason: "No corroboratable stopped limit turn"}
	}
	model := ModelClass(e.Model)
	if model == "" || (e.Group != "" && e.Group != model) {
		return Decision{Action: "unknown", Reason: "Session model compatibility is unknown"}
	}
	if in.Wait < 15*time.Minute {
		return Decision{Action: "unknown", Reason: "Wait must be at least 15 minutes"}
	}
	if e.AccountID == "" || e.AccountIdentity == "" || e.AccountID != in.CurrentAccount {
		return Decision{Action: "unknown", Reason: "Stopped incident account is unknown or changed; retry in the current source account"}
	}
	quotaModel := ModelFamily(e.Model)
	sourceBlocked := false
	sourceKnown := false
	for _, a := range in.Accounts {
		if a.ID == in.CurrentAccount {
			if a.Identity != e.AccountIdentity {
				return Decision{Action: "unknown", Reason: "Stopped incident account identity changed"}
			}
			_, sourceKnown, sourceBlocked = accountReady(a, quotaModel, in.Now)
			break
		}
	}
	if !sourceKnown {
		return Decision{Action: "unknown", Reason: "Refresh applicable source quota and ownership"}
	}
	if !sourceBlocked {
		return Decision{Action: "ignore", Reason: "Applicable account quota is not exhausted"}
	}
	var earliest time.Time
	unknown := false
	eligible := 0
	for _, a := range in.Accounts {
		if !a.Eligible {
			continue
		}
		eligible++
		ready, known, _ := accountReady(a, quotaModel, in.Now)
		if !known {
			unknown = true
			continue
		}
		if !ready.After(in.Now) {
			return Decision{Action: "rotate", Account: a.ID, Reason: "Compatible account is usable now"}
		}
		if earliest.IsZero() || ready.Before(earliest) {
			earliest = ready
		}
	}
	if eligible == 0 {
		return Decision{Action: "unknown", Reason: "No eligible compatible resources"}
	}
	if unknown {
		return Decision{Action: "unknown", Reason: "Refresh stale quota, reset times, or uncertain ownership"}
	}
	if earliest.IsZero() {
		return Decision{Action: "unknown", Reason: "Reset time is unknown"}
	}
	d := Decision{Action: "wait", ReadyAt: earliest, Reason: "Compatible account expected within configured wait"}
	if earliest.Sub(in.Now) <= in.Wait {
		return d
	}
	if !in.DestinationUsable {
		d.Reason = "Destination is unavailable; wait for compatible account"
		return d
	}
	d.Action = "offer"
	d.Reason = "All eligible compatible accounts are blocked beyond configured wait"
	return d
}
