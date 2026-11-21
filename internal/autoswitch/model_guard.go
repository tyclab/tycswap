package autoswitch

import (
	"sort"
	"strings"

	"github.com/tyclab/tycswap/internal/oauth"
)

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
	if len(required) == 0 {
		return true
	}
	u := oauth.NewUsage(usageDict(value))
	if u == nil {
		return false
	}
	present := map[string]bool{}
	for _, w := range u.Scoped {
		present[strings.ToLower(w.Name)] = true
	}
	for _, name := range oauth.ResolveModelWindows(u, required) {
		if !present[strings.ToLower(name)] {
			return false
		}
	}
	return true
}
