package authfile

// codex-auth schema v4 renames, applied as ONE lookup: applied in sequence, "team" would land on "enterprise".
var planRenames = map[string]string{
	"team":     "business",
	"business": "enterprise",
}

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
