package reporting

type RunResult struct {
	OK      bool
	Message string
	Payload any
}

// RunAction projects (payload, error) instead of capturing ANSI stdout (DESIGN Deviation #7): modal text is not Python's, by design.
func RunAction(fn func() (any, error)) RunResult {
	payload, err := fn()
	if err != nil {
		return RunResult{OK: false, Message: "Error: " + err.Error()}
	}
	return RunResult{OK: true, Payload: payload}
}
