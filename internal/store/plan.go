package store

import (
	"encoding/json"
	"os"

	"github.com/tyclab/tycswap/internal/ccfile"
)

// RecordPlan is the plan a roster record names (DESIGN A79): its "plan"
// value once add or a switch has written one; before that, the plan in the
// slot's config backup, or in the live config when the record is the live
// login. Nothing is written back.
func (s *Store) RecordPlan(num string, rec map[string]any) string {
	if plan, present := rec["plan"]; present {
		p, _ := plan.(string)
		return p
	}
	email := strField(rec, "email")
	if email == "" {
		return ""
	}
	if liveEmail, livePlan := s.liveOAuthPlan(); liveEmail != "" && liveEmail == email {
		return livePlan
	}
	configText, _ := s.ReadAccountConfig(num, email)
	return ccfile.PlanDisplayName(configText)
}

// liveOAuthPlan reads the live login's email and plan from ~/.claude.json,
// blanks on any failure.
func (s *Store) liveOAuthPlan() (email, plan string) {
	raw, err := os.ReadFile(s.GlobalConfigPath())
	if err != nil {
		return "", ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return "", ""
	}
	oauth, _ := m["oauthAccount"].(map[string]any)
	return strOrEmpty(oauth["emailAddress"]), ccfile.PlanDisplayName(string(raw))
}
