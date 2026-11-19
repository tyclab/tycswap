package store

import (
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
)

// FableMarkKey is the roster key `tycswap fable --allow` writes: the
// operator's word that the account runs Fable (DESIGN A107).
const FableMarkKey = "fableContinue"

// What decides whether an account runs Fable.
const (
	FableByPlan  = "plan"    // the plan: Max or Ultra runs it, Pro and Free do not
	FableByMark  = "mark"    // the roster's FableMarkKey
	FableUnknown = "unknown" // neither: the plan names no answer and nothing is marked
	FableAPIKey  = "api-key" // an API-key account never runs Fable here
)

// FableAnswer is FableCapable's answer, what decided it, and the plan read.
type FableAnswer struct {
	Capable bool
	By      string
	Plan    string
}

// FableEntitlement is FableCapable with its reason, from a loaded roster. A
// plan without Fable (Pro, Free) decides before a mark; otherwise a boolean
// mark decides before the plan (DESIGN A71, A107).
func (s *Store) FableEntitlement(data *SequenceData, number string) FableAnswer {
	rec, ok := recordFor(data, number)
	if !ok {
		return FableAnswer{By: FableUnknown}
	}
	if strField(rec, "kind") == "api_key" {
		return FableAnswer{By: FableAPIKey}
	}
	creds, _ := s.defaultScope().ReadAccountCredentials(number, strField(rec, "email"))
	plan := strField(rec, "subscriptionType")
	if value, ok := oauth.ExtractOAuthData(creds)["subscriptionType"].(string); ok {
		plan = value
	}
	var mark *bool
	if value, ok := rec[FableMarkKey].(bool); ok {
		mark = &value
	}
	byPlan := groups.Entitlement(groups.Fable, groups.Continue, plan, nil, nil)
	answer := FableAnswer{Capable: groups.Entitlement(groups.Fable, groups.Continue, plan, nil, mark).Allowed, Plan: plan}
	switch {
	case byPlan.Known && !byPlan.Allowed:
		answer.By = FableByPlan
	case mark != nil:
		answer.By = FableByMark
	case byPlan.Known:
		answer.By = FableByPlan
	default:
		answer.By = FableUnknown
	}
	return answer
}
