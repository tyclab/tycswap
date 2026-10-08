package session

// NoOp: the account is already the active default login with no CLAUDE_CONFIG_DIR preset (FINDING 1); env emits nothing.
type EnvResult struct {
	Dir        string
	AccountNum string
	Email      string
	Scrubbed   []string
	NoOp       bool
}

// SetupEnv reuses Run's preamble and bootstrap exactly but never execs; notices go to the Stdout sink, which env routes to stderr.
func (m *Manager) SetupEnv(identifier string, share, shareHistory bool) (EnvResult, error) {
	_, accountNum, email, sameActive, err := m.setupPreamble(identifier, nil, shareHistory, envMode)
	if err != nil {
		return EnvResult{}, err
	}
	if sameActive {
		return EnvResult{AccountNum: accountNum, Email: email, NoOp: true}, nil
	}

	sessionDir, accountNum, email, scrubbed, err := m.setupBootstrap(identifier, share, shareHistory, envMode)
	if err != nil {
		return EnvResult{}, err
	}

	return EnvResult{Dir: sessionDir, AccountNum: accountNum, Email: email, Scrubbed: scrubbed}, nil
}
