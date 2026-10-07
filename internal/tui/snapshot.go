package tui

import (
	"errors"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
)

// Facade is the account-operation seam the TUI drives (DESIGN §2.20, FROZEN by
// A13). *core.Switcher satisfies it; cli carries the compile assertion
// var _ tui.Facade = (*core.Switcher)(nil). Method shapes are verbatim from the
// design — do not extend without changing both here and core (A13).
type Facade interface {
	AccountsSnapshot(fetch map[string]bool) *reporting.AccountsSnapshot
	SwitchTo(id string, jsonOut bool) (map[string]any, error)
	Switch(strategy *string, jsonOut bool, models []string, modelSrc *string) (map[string]any, error)
	SetAccountDisabled(id string, disabled bool) error
	RemoveAccount(id string, yes bool) error
	AddAccount(slot *int, assumeYes bool, alias *string) error
	AddAccountFromToken(token string, email, slotArg *string, assumeYes bool) error
	BackupDir() string
	SetPollPolicyInputs(threshold float64, models []string)
	ClearPollPolicyInputs()
}

// baseURLAdder is add-token with a base URL (DESIGN A46). It is not on the
// frozen Facade: the TUI asks for it by type assertion, and *core.Switcher
// provides it.
type baseURLAdder interface {
	AddAccountFromTokenWithBaseURL(token, baseURL string, email, slotArg *string, assumeYes bool) error
}

var errNoBaseURL = errors.New("this build cannot store a base URL with a token")

type snapshotSource struct {
	facade Facade
}

// take runs one blocking snapshot pass. full does not change the fetch set
// (09§6.1, test_every_pass_is_store_governed): store_only → an empty (non-nil)
// fetch set; otherwise nil (every stale account eligible).
func (s snapshotSource) take(full, storeOnly bool) *reporting.AccountsSnapshot {
	var fetch map[string]bool
	if storeOnly {
		fetch = map[string]bool{}
	}
	return s.facade.AccountsSnapshot(fetch)
}

type actionResult struct {
	OK      bool
	Message string
	Payload map[string]any
	// Warning is a second toast at warning severity (a Codex switch reporting
	// running codex sessions, multiprovider.go); "" for every Facade action.
	Warning string
}

func (r actionResult) firstLine() string { return r.Message }

func runAction(fn func() (map[string]any, error)) actionResult {
	payload, err := fn()
	if err != nil {
		return actionResult{OK: false, Message: "Error: " + err.Error()}
	}
	return actionResult{OK: true, Payload: payload}
}

// refreshDoneMsg carries a completed snapshot pass back to Update (the
// "refresh" worker group). reporting.Snapshot never errors, so there is no
// refreshErr counterpart.
type refreshDoneMsg struct {
	snap   *reporting.AccountsSnapshot
	owners rowOwners
}

type pollTickMsg struct{}

type actionDoneMsg struct {
	label      string
	result     actionResult
	showOutput bool
}

type engineEventMsg struct {
	gen int // engine generation; a stale generation's events are dropped
	ev  autoswitch.Event
}

type engineStoppedMsg struct {
	gen  int
	code int
	err  error
}

type flashClearMsg struct {
	number string
	token  int
}

type toastExpireMsg struct{ id int }

type AutoEngine interface {
	RunLoop() int
	Stop()
	Wake()
	ApplyThreshold(threshold float64)
}

type EngineFactory func(s settings.AutoSwitchSettings, onEvent func(autoswitch.Event), dryRun bool) AutoEngine
