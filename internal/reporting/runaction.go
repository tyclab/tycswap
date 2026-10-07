package reporting

type RunResult struct {
	OK      bool
	Message string
	Payload any
}

func RunAction(fn func() (any, error)) RunResult {
	payload, err := fn()
	if err != nil {
		return RunResult{OK: false, Message: "Error: " + err.Error()}
	}
	return RunResult{OK: true, Payload: payload}
}
