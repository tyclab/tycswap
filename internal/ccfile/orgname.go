package ccfile

import (
	"encoding/json"
	"strings"
)

// PlaceholderOrgName reports whether name is the organization name Claude.ai
// generates for a personal account, "<email>'s Organization" (DESIGN A79).
func PlaceholderOrgName(email, name string) bool {
	if email == "" || name == "" {
		return false
	}
	name = strings.TrimSpace(name)
	for _, apostrophe := range []string{"'", "’"} {
		if strings.EqualFold(name, email+apostrophe+"s Organization") {
			return true
		}
	}
	return false
}

// DisplayOrgName is name for display: "" when it is the placeholder.
func DisplayOrgName(email, name string) string {
	if PlaceholderOrgName(email, name) {
		return ""
	}
	return name
}

// PlanDisplayName is the plan Claude Code recorded for the login in a
// .claude.json text (oauthAccount.planDisplayName, e.g. "Ultra"), or "".
func PlanDisplayName(configText string) string {
	var m struct {
		OAuthAccount struct {
			PlanDisplayName string `json:"planDisplayName"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal([]byte(configText), &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.OAuthAccount.PlanDisplayName)
}
