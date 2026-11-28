package oauth

import "testing"

func TestTokensWiped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		creds string
		want  bool
	}{
		{"both tokens emptied in place", `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":4102444800000,"scopes":["user:inference"]}}`, true},
		{"token fields gone", `{"claudeAiOauth":{"expiresAt":4102444800000},"mcpOAuth":{}}`, true},
		{"a full login", `{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt"}}`, false},
		{"access token only (setup token)", `{"claudeAiOauth":{"accessToken":"at"}}`, false},
		{"refresh token only", `{"claudeAiOauth":{"refreshToken":"rt"}}`, false},
		{"not JSON", "not json", false},
		{"no login object", `{"mcpOAuth":{}}`, true},
		{"blank access token, refresh token kept", `{"claudeAiOauth":{"accessToken":"","refreshToken":"rt","expiresAt":1}}`, false},
		{"API key", "sk-ant-api03-key", false},
		{"empty", "", false},
	} {
		if got := TokensWiped(tc.creds); got != tc.want {
			t.Errorf("%s: TokensWiped = %v, want %v", tc.name, got, tc.want)
		}
	}
}
