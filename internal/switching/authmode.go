// authmode.go — moving Claude Code onto an API-key account changes HOW it
// authenticates, and this package refuses to make that change unless the
// caller says the user approved it (DESIGN A33).
//
// Switching between subscription accounts rewrites a credential, and a running
// Claude Code session picks that up by itself. An API-key account instead
// replaces the subscription login with a managed key billed per token, and a
// Claude Code session that is already running keeps the login it started with
// until it is restarted. So this must never happen as a side effect of a
// rotation, an engine tick or an HTTP call that nobody confirmed.
//
// The guard lives here, at the one place every front-end funnels through, so
// refusing is the default and an approval has to be supplied on purpose.
package switching

import (
	"fmt"
	"sync"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
)

// ErrAPIKeyNeedsApproval is what a caller without an approval gets back. The
// message names the restart, because that is the part people do not expect.
func ErrAPIKeyNeedsApproval(num string) error {
	return cerr.Validation(
		"Account-%s authenticates with an API key. Switching to it changes how Claude Code authenticates, "+
			"and every Claude Code session that is already running keeps its current login until you restart it. "+
			"Confirm the switch to go ahead: `tycswap switch %s` asks first.", num, num)
}

// APIKeyRestartNote follows a switch onto an API-key account in place of the
// usual "no restart needed" note.
const APIKeyRestartNote = "Restart your Claude Code sessions: one that is already running keeps its previous login until it is restarted."

// RestartNotice is the sentence every front-end shows with the question it
// asks before a switch onto an API-key account. running is the number of
// Claude Code sessions found under the default config directory; it is named
// because "restart Claude Code" reads as optional until you know how many
// sessions that means.
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

// approval holds the opt-ins front-ends record after the user said yes. It is
// explicit state rather than a parameter: SwitchTo's two-argument shape is
// pinned by the autoswitch.Switcher and tui.Facade interfaces (DESIGN
// §2.18/§2.20), and widening it would ripple through code that has no business
// knowing about auth modes.
var approval struct {
	sync.Mutex
	targets map[string]bool
}

// ApproveAPIKeySwitch records that the user approved switching onto slot num.
// The approval is consumed by the next switch to that slot and by nothing
// else.
func ApproveAPIKeySwitch(num string) {
	approval.Lock()
	defer approval.Unlock()
	if approval.targets == nil {
		approval.targets = map[string]bool{}
	}
	approval.targets[num] = true
}

// takeApproval consumes an approval for num, if one is pending.
func takeApproval(num string) bool {
	approval.Lock()
	defer approval.Unlock()
	if approval.targets[num] {
		delete(approval.targets, num)
		return true
	}
	return false
}

// clearApprovals drops every pending approval (tests).
func clearApprovals() {
	approval.Lock()
	defer approval.Unlock()
	approval.targets = nil
}

// ErrEndpointNeedsApproval is ErrAPIKeyNeedsApproval for an API-key account
// with a base URL (DESIGN A46): the message also names where Claude Code's
// requests would go.
func ErrEndpointNeedsApproval(num, host string) error {
	return cerr.Validation(
		"Account-%s authenticates with an API key at %s. Switching to it changes how Claude Code authenticates "+
			"and where it sends its requests, also for a Claude Code session that is already running once it re-reads its settings. "+
			"Confirm the switch to go ahead: `tycswap switch %s` asks first.", num, host, num)
}

// EndpointNotice is the sentence every front-end adds to its question before
// a switch onto an API-key account with a base URL: where the requests go,
// and which two settings carry it there and back.
func EndpointNotice(baseURL string) string {
	return "This account sends Claude Code's requests to " + baseURL + ": the switch writes env.ANTHROPIC_BASE_URL and " +
		"env.ANTHROPIC_AUTH_TOKEN into Claude Code's settings.json and removes env.ANTHROPIC_API_KEY, and a switch to another " +
		"account puts back what they held."
}

// EndpointSessionNotice is RestartNotice for a switch onto an account with a
// base URL (DESIGN A46): a running session re-reads settings.json and takes
// the endpoint up then (at once in a trusted workspace), so it does not keep
// its login until a restart the way A33's notice says for a managed key.
// running counts the sessions, as there.
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

// guardAPIKeyTarget returns an error when num is an API-key account the user
// has not approved switching to. approved is the approval SwitchTo took for
// num when it resolved the target, so it is used up whatever the kind.
func guardAPIKeyTarget(s *store.Store, num string, approved bool) error {
	if s.AccountKindFor(num) != "api_key" || approved {
		return nil
	}
	if base := s.AccountBaseURL(num); base != "" {
		return ErrEndpointNeedsApproval(num, termsafe.Strip(ccsettings.Host(base)))
	}
	return ErrAPIKeyNeedsApproval(num)
}
