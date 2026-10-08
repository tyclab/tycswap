// authmode.go — switching onto an API-key account changes HOW Claude Code authenticates, and running sessions keep their old login
// until restarted, so it must never happen as a side effect of a rotation, a tick or an unconfirmed HTTP call (DESIGN A33).
// The guard sits where every front-end funnels through: refusing is the default and an approval must be supplied on purpose.
package switching

import (
	"fmt"
	"sync"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
)

func ErrAPIKeyNeedsApproval(num string) error {
	return cerr.Validation(
		"Account-%s authenticates with an API key. Switching to it changes how Claude Code authenticates, "+
			"and every Claude Code session that is already running keeps its current login until you restart it. "+
			"Confirm the switch to go ahead: `tycswap switch %s` asks first.", num, num)
}

const APIKeyRestartNote = "Restart your Claude Code sessions: one that is already running keeps its previous login until it is restarted."

func RestartNotice(running int) string {
	switch {
	case running == 1:
		return "1 Claude Code session is running; it keeps its current login until you restart it."
	case running > 1:
		return fmt.Sprintf("%d Claude Code sessions are running; they keep their current login until each one is restarted.", running)
	default:
		return "Any Claude Code session that is already running keeps its current login until you restart it."
	}
}

// Explicit state, not a parameter: SwitchTo's two-argument shape is pinned by autoswitch.Switcher and tui.Facade.
var approval struct {
	sync.Mutex
	targets map[string]bool
}

// The approval is consumed by the next switch to that slot and by nothing else.
func ApproveAPIKeySwitch(num string) {
	approval.Lock()
	defer approval.Unlock()
	if approval.targets == nil {
		approval.targets = map[string]bool{}
	}
	approval.targets[num] = true
}

func takeApproval(num string) bool {
	approval.Lock()
	defer approval.Unlock()
	if approval.targets[num] {
		delete(approval.targets, num)
		return true
	}
	return false
}

func clearApprovals() {
	approval.Lock()
	defer approval.Unlock()
	approval.targets = nil
}

func ErrEndpointNeedsApproval(num, host string) error {
	return cerr.Validation(
		"Account-%s authenticates with an API key at %s. Switching to it changes how Claude Code authenticates "+
			"and where it sends its requests, also for a Claude Code session that is already running once it re-reads its settings. "+
			"Confirm the switch to go ahead: `tycswap switch %s` asks first.", num, host, num)
}

func EndpointNotice(baseURL string) string {
	return "This account sends Claude Code's requests to " + baseURL + ": the switch writes env.ANTHROPIC_BASE_URL and " +
		"env.ANTHROPIC_AUTH_TOKEN into Claude Code's settings.json and removes env.ANTHROPIC_API_KEY, and a switch to another " +
		"account puts back what they held."
}

// A running session re-reads settings.json and takes the endpoint up then, unlike A33's managed key (DESIGN A46).
func EndpointSessionNotice(running int) string {
	const when = "when it re-reads settings.json (at once in a trusted workspace), or when it is restarted."
	switch {
	case running == 1:
		return "1 Claude Code session is running; it takes the endpoint up " + when
	case running > 1:
		return fmt.Sprintf("%d Claude Code sessions are running; each takes the endpoint up ", running) + when
	default:
		return "A Claude Code session that is already running takes the endpoint up " + when
	}
}

// EndpointAppliedNote follows a switch that wrote an endpoint into Claude
// Code's settings.json. Claude Code applies the file's env block when it
// starts and again when a running session sees the file change (in a
// trusted workspace), so a session that is already running can move to the
// endpoint at once.
func EndpointAppliedNote(host string) string {
	return "Claude Code's settings.json now sends its requests to " + host + " (env.ANTHROPIC_BASE_URL, env.ANTHROPIC_AUTH_TOKEN); " +
		"a switch to another account puts back what it held. A Claude Code session that is already running takes this up " +
		"when it re-reads settings.json (at once in a trusted workspace); restart one that does not."
}

// EndpointRevertedNote follows a switch that took an endpoint back out of
// Claude Code's settings.json. A running session re-applies the env block by
// adding keys, never removing one, so it keeps the endpoint and its key until
// it is restarted.
const EndpointRevertedNote = "Claude Code's settings.json no longer sends its requests to an API-key account's endpoint. " +
	"Restart your Claude Code sessions: one that is already running keeps the endpoint and its key until it is restarted."

func guardAPIKeyTarget(s *store.Store, num string, approved bool) error {
	if s.AccountKindFor(num) != "api_key" || approved {
		return nil
	}
	if base := s.AccountBaseURL(num); base != "" {
		return ErrEndpointNeedsApproval(num, termsafe.Strip(ccsettings.Host(base)))
	}
	return ErrAPIKeyNeedsApproval(num)
}
