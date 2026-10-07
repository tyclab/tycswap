package groups

import (
	"fmt"
	"path/filepath"
	"strings"
)

type ID string

const (
	Fable ID = "fable"
	Opus  ID = "opus"
)

func Parse(value string) (ID, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fable":
		return Fable, nil
	case "opus", "other", "opus/other":
		return Opus, nil
	default:
		return "", fmt.Errorf("unknown session group %q; choose fable or opus", value)
	}
}

func All() []ID { return []ID{Fable, Opus} }

func (id ID) Label() string {
	if id == Fable {
		return "Fable"
	}
	return "Opus / other"
}

func ScopeDir(root string, id ID) string   { return filepath.Join(root, "groups", string(id)) }
func ProfileDir(root string, id ID) string { return filepath.Join(ScopeDir(root, id), "profile") }
func StatePath(root string, id ID) string {
	return filepath.Join(ScopeDir(root, id), "autoswitch_state.json")
}
func ActivePath(root string, id ID) string { return filepath.Join(ScopeDir(root, id), "active.json") }
func JournalPath(root string, id ID) string {
	return filepath.Join(ScopeDir(root, id), "transaction.json")
}

func CheckModel(id ID, model string) error {
	if _, err := Parse(string(id)); err != nil {
		return err
	}
	m := strings.ToLower(strings.TrimSpace(model))
	for _, ch := range m {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
			return fmt.Errorf("model %q has unknown group compatibility", model)
		}
	}
	if m == "" || m == "default" || m == "inherit" {
		return fmt.Errorf("model compatibility is unknown; select an explicit model for the %s group", id)
	}
	fable := m == "fable" || strings.HasPrefix(m, "claude-fable-")
	if (id == Fable) != fable {
		return fmt.Errorf("model %q belongs to another session group; restart or resume into that group", model)
	}
	if id == Opus && !(m == "opus" || m == "sonnet" || m == "haiku" || strings.HasPrefix(m, "claude-opus-") || strings.HasPrefix(m, "claude-sonnet-") || strings.HasPrefix(m, "claude-haiku-")) {
		return fmt.Errorf("model %q has unknown group compatibility", model)
	}
	return nil
}

type Account struct {
	Number  string `json:"number"`
	Email   string `json:"email"`
	OrgUUID string `json:"organizationUuid"`
	UUID    string `json:"uuid,omitempty"`
}

func (a Account) SameIdentity(b Account) bool { return a.Email == b.Email && a.OrgUUID == b.OrgUUID }

type Owner struct {
	Scope      string  `json:"scope"`
	ProfileDir string  `json:"profileDir"`
	Account    Account `json:"account"`
	Uncertain  bool    `json:"uncertain,omitempty"`
	PIDs       []int   `json:"pids,omitempty"`
}

type Status struct {
	ID           ID      `json:"id"`
	Label        string  `json:"label"`
	ActiveNumber *string `json:"activeNumber"`
	Email        string  `json:"email,omitempty"`
	ProfileDir   string  `json:"profileDir"`
	Enabled      bool    `json:"enabled"`
	LiveSessions int     `json:"liveSessions"`
	Pending      bool    `json:"pending"`
	Blocker      string  `json:"blocker,omitempty"`
}

type Intent string

const (
	Start    Intent = "start"
	Continue Intent = "continue"
)

type Compatibility struct {
	Known   bool   `json:"known"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

func Entitlement(id ID, intent Intent, plan string, start, continuation *bool) Compatibility {
	if id == Opus {
		return Compatibility{Known: true, Allowed: true}
	}
	plan = strings.ToLower(strings.TrimSpace(plan))
	if plan == "pro" || plan == "free" {
		return Compatibility{Known: true, Reason: "this plan cannot run Fable"}
	}
	var explicit *bool
	if intent == Continue {
		explicit = continuation
	} else {
		explicit = start
	}
	if explicit != nil {
		return Compatibility{Known: true, Allowed: *explicit, Reason: "recorded Fable " + string(intent) + " capability"}
	}
	if plan == "max" || strings.HasPrefix(plan, "max_") {
		return Compatibility{Known: true, Allowed: true}
	}
	return Compatibility{Reason: "Fable " + string(intent) + " entitlement is unknown; quota alone does not establish permission"}
}
