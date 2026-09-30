// plans.go — Codex plan-tier naming. Implements claude-swap PR #252
// codex/plans.py.
//
// codex-auth's schema v4 renamed the tiers to final product semantics. Records
// imported from an older registry — and the plan_type the usage API returns —
// are normalized through here so one Business account never displays as
// Enterprise in one view and Business in another.

package authfile

// planRenames holds v4's renames, applied as ONE lookup. Applied
// sequentially, a legacy "team" would pass through "business" and land on
// "enterprise" — every Business account mislabelled one tier up.
var planRenames = map[string]string{
	"team":     "business",
	"business": "enterprise",
}

// NormalizePlan maps a stored or reported plan tier onto v4's final semantics.
// Anything that is not a non-empty string normalizes to "".
func NormalizePlan(plan any) string {
	s, ok := plan.(string)
	if !ok || s == "" {
		return ""
	}
	if renamed, ok := planRenames[s]; ok {
		return renamed
	}
	return s
}
