package lifecycle

import (
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
)

// add refuses a live login whose tokens Claude Code wiped (DESIGN A130).
func TestAddRefusesAWipedLiveLogin(t *testing.T) {
	s := newStore(t)
	seed(t, s, ip(1), acct{num: "1", email: "alice@example.com", creds: oauthBlob, config: `{"oauthAccount":{"emailAddress":"alice@example.com"}}`})
	seedLiveLogin(t, s, "alice@example.com", "", "", "uuid-a", `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":4102444800000}}`)
	err := AddAccount(s, nil, false, nil)
	if err == nil || cerr.TypeName(err) != "ValidationError" || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("AddAccount = %v, want a validation error asking to sign in again", err)
	}
	if c, _ := s.ReadAccountCredentials("1", "alice@example.com"); c != oauthBlob {
		t.Errorf("Account-1's stored login changed to %q", c)
	}
}
