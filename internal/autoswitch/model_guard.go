package autoswitch

import (
	"sort"
	"strings"

	"github.com/tyclab/tycswap/internal/oauth"
)

func reportedModels(value any) map[string]bool {
	names := map[string]bool{}
	if u := oauth.NewUsage(usageDict(value)); u != nil {
		for _, w := range u.Scoped {
			names[strings.ToLower(w.Name)] = true
		}
	}
	return names
}

func requiredModels(models []string) []string {
	names := map[string]bool{}
	for _, model := range models {
		names[strings.ToLower(model)] = true
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func hasRequiredModels(value any, required []string) bool {
	present := reportedModels(value)
	for _, name := range required {
		if !present[name] {
			return false
		}
	}
	return true
}
