// addlogin.go — "Add current login": store the login Claude Code has. The
// dashboard's button and the tray's row both come here (DESIGN A37), so the
// two say the same about what happened.
package web

import "github.com/tyclab/tycswap/internal/cerr"

// ErrNoLogin: Claude Code has no subscription login to store. The tray and
// the dashboard both say it, before anything is tried.
var ErrNoLogin = cerr.Config("Claude Code has no subscription login on this computer. In Claude Code, type /login, choose Claude.ai Subscription and sign in, then choose Add current login again.")

// AddLoginResult is what AddCurrentLogin did. Number and Email are the
// account the login is now; Refreshed: it was one already.
type AddLoginResult struct {
	Number    string `json:"number"`
	Email     string `json:"email"`
	Refreshed bool   `json:"refreshed"`
}

// noFetch asks the snapshot for no usage: only who the accounts are counts here.
var noFetch = map[string]bool{}

// AddCurrentLogin stores the login Claude Code is signed in with (the CLI's
// `add`) under the dashboard's mutation lock and broadcasts the new state.
// Without a live login (Deps.CurrentLogin) it is ErrNoLogin.
func (s *Server) AddCurrentLogin() (AddLoginResult, error) {
	var res AddLoginResult
	if s.d.CurrentLogin != nil {
		if email, ok := s.d.CurrentLogin(); !ok || email == "" {
			return res, ErrNoLogin
		}
	}
	s.mutMu.Lock()
	before := s.d.Facade.AccountsSnapshot(noFetch)
	err := s.d.Facade.AddAccount(nil, true, nil)
	after := s.d.Facade.AccountsSnapshot(noFetch)
	s.mutMu.Unlock()
	defer s.broadcast()
	if err != nil {
		return res, err
	}
	if after != nil {
		for _, a := range after.Accounts {
			if a.IsActive {
				res.Number, res.Email = a.Number, a.Email
			}
		}
	}
	// Refreshed: the slot held this account before the add.
	if before != nil && res.Number != "" {
		for _, a := range before.Accounts {
			if a.Number == res.Number && a.Email == res.Email {
				res.Refreshed = true
			}
		}
	}
	return res, nil
}
