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

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/store"
)

// ErrAPIKeyNeedsApproval is what a caller without an approval gets back. The
// message names the restart, because that is the part people do not expect.
func ErrAPIKeyNeedsApproval(num string) error {
	return cerr.Validation(
		"Account-%s authenticates with an API key. Switching to it changes how Claude Code authenticates, "+
			"and every Claude Code session that is already running keeps its current login until you restart it. "+
			"Confirm the switch to go ahead.", num)
}

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

// guardAPIKeyTarget returns an error when num is an API-key account the user
// has not approved switching to.
func guardAPIKeyTarget(s *store.Store, num string) error {
	if s.AccountKindFor(num) != "api_key" {
		return nil
	}
	if takeApproval(num) {
		return nil
	}
	return ErrAPIKeyNeedsApproval(num)
}
