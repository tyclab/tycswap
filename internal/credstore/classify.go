// Credential classifiers (spec 03§5.2, 01§3.7).

package credstore

import "strings"

func LooksLikeAPIKey(credentials string) bool {
	if credentials == "" {
		return false
	}
	text := strings.TrimSpace(credentials)
	return strings.HasPrefix(text, "sk-ant-api") && !strings.HasPrefix(text, "{")
}

func ApprovedForm(apiKey string) string {
	t := strings.TrimSpace(apiKey)
	if len(t) <= 20 {
		return t
	}
	return t[len(t)-20:]
}
