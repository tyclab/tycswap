// addlogin.go: the dashboard button and the tray row both come here (DESIGN A37), so they report the same outcome.

package web

import (
	"net/http"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/lifecycle"
)

// ErrNoLogin: Claude Code has no subscription login to store. The tray and
// the dashboard both say it, before anything is tried.
var ErrNoLogin = cerr.Config("Claude Code has no subscription login on this computer. In Claude Code, type /login, choose Claude.ai Subscription and sign in, then choose Add current login again.")

// AddLoginResult is what AddCurrentLogin did. Number and Email are the
// account the login is now; Refreshed: it was one already.
type AddLoginResult = lifecycle.AddLoginResult

// AddCurrentLogin stores the login Claude Code is signed in with (the CLI's
// `add`) under the dashboard's mutation lock and broadcasts the new state.
// Without a live login (Deps.CurrentLogin) it is ErrNoLogin.
func (s *Server) AddCurrentLogin() (AddLoginResult, error) {
	if s.d.CurrentLogin != nil {
		if email, ok := s.d.CurrentLogin(); !ok || email == "" {
			return AddLoginResult{}, ErrNoLogin
		}
	}
	saver, ok := s.d.Facade.(interface {
		SaveCurrentLogin() (AddLoginResult, error)
	})
	if !ok {
		return AddLoginResult{}, httpErr(http.StatusServiceUnavailable, "Saving the current login is unavailable")
	}
	defer s.broadcast()
	s.mutMu.Lock()
	defer s.mutMu.Unlock()
	return saver.SaveCurrentLogin()
}
