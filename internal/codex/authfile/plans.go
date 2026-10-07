package authfile

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
