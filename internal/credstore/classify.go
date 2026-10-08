// Credential classifiers (spec 03§5.2, 01§3.7).

package credstore

import "strings"

// Strict: requiring sk-ant-api and no JSON keeps a raw sk-ant-oat setup token from being misclassified.
func LooksLikeAPIKey(credentials string) bool {
	if credentials == "" {
		return false
	}
	text := strings.TrimSpace(credentials)
	return strings.HasPrefix(text, "sk-ant-api") && !strings.HasPrefix(text, "{")
}

// Claude Code stores the stripped key's last 20 characters (normalizeApiKeyForConfig = apiKey.slice(-20)).
func ApprovedForm(apiKey string) string {
	t := strings.TrimSpace(apiKey)
	if len(t) <= 20 {
		return t
	}
	return t[len(t)-20:]
}
