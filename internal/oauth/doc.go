package oauth

const (
	OAuthBetaHeader     = "oauth-2025-04-20"
	OAuthExpiryBufferMS = 5 * 60 * 1000
	OAuthTokenURL       = "https://platform.claude.com/v1/oauth/token"
	OAuthClientID       = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	userAgent = "claude-swap/1.0"
)

var (
	profileURL = "https://api.anthropic.com/api/oauth/profile"
	usageURL   = "https://api.anthropic.com/api/oauth/usage"
)

const (
	ErrInvalidGrant = "invalid_grant"
	// ErrNoRefreshToken means the credential carries no usable refresh token.
	ErrNoRefreshToken = "no_refresh_token"
	ErrTransient      = "transient"
	ErrNoAccessToken  = "no-access-token"
	ErrRefreshFailed  = "refresh-failed"
	ErrTimeout        = "timeout"
	ErrNetwork        = "network"
	ErrBadResponse    = "bad-response"
)
