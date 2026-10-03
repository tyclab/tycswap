// authmode.go — one explicit approval in front of a switch onto an API-key
// account, the one switch that changes HOW Claude Code authenticates (DESIGN
// A33).
//
// Switching which subscription account is active is cheap and reversible: the
// credential is rewritten and a running session picks it up on its own.
// Moving onto an API-key account is not: it replaces the subscription login
// with a managed key billed per token, and a Claude Code session that is
// already running keeps the login it started with until it is restarted. The
// switch layer refuses such a target without an approval
// (switching.ApproveAPIKeySwitch); this file is where the command line asks
// for one, naming the sessions the user will have to restart.
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/switching"
)

// runningSessions counts the Claude Code sessions alive under the default
// Claude config directory, the login a switch rewrites. A detection failure
// counts as zero: the notice is advisory, and a wrong zero is better than
// refusing the operation.
var runningSessions = func() int {
	return len(procdetect.ListSessions(procdetect.GetClaudeDir()))
}

// restartNotice is the sentence every auth-mode change ends with. It names the
// number of sessions because "restart Claude Code" reads as optional until you
// know how many are affected.
func restartNotice() string {
	return switching.RestartNotice(runningSessions())
}

// confirmAuthModeChange asks before changing how Claude Code authenticates.
// question is the one-line ask, detail says what changes. assumeYes (--yes)
// skips the prompt but still prints the notice, so a scripted run leaves the
// same trace in the output.
//
// A non-interactive terminal cannot answer, so it is refused rather than
// silently approved: this is exactly the operation that must not happen by
// accident.
func confirmAuthModeChange(out io.Writer, question, detail string, assumeYes bool) bool {
	fmt.Fprintln(out, detail)
	fmt.Fprintln(out, restartNotice())
	if assumeYes {
		return true
	}
	answer, ok := lifecycle.ActivePrompter.Prompt(question + " [y/N] ")
	if !ok {
		fmt.Fprintln(out, "Not a terminal — rerun with --yes to confirm.")
		return false
	}
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}

// confirmSwitchToAPIKey gates `switch <num|email>` onto an API-key account. It
// returns stop=true when the user declined, in which case the caller must not
// switch. Anything it cannot resolve (an unknown identifier, a store error) is
// left to the switch itself to report, so error messages stay in one place.
//
// --json is never prompted, since a machine-readable run has no one to ask:
// --yes approves it, and without --yes the switch layer refuses the target
// with its approval error. Without --json and without a terminal, the prompt
// refuses and names --yes, the flag that exists to answer it.
func confirmSwitchToAPIKey(out io.Writer, identifier string, sw *core.Switcher, jsonOut, assumeYes bool) (stop bool) {
	num, _, _, err := sw.Store.ResolveAccount(identifier)
	if err != nil || num == "" || sw.Store.AccountKindFor(num) != "api_key" {
		return false
	}
	if jsonOut {
		if assumeYes {
			switching.ApproveAPIKeySwitch(num)
		}
		return false
	}
	if confirmAuthModeChange(out,
		"Switch to API-key account #"+num+"?",
		"An API-key account authenticates with a key instead of a subscription login, and its usage is billed per token.",
		assumeYes) {
		switching.ApproveAPIKeySwitch(num)
		return false
	}
	fmt.Fprintln(out, "Cancelled.")
	return true
}
