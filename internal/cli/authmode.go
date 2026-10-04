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

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/termsafe"
)

// stdinIsTerminal reports whether stdin can answer a prompt (a terminal, not
// /dev/null or a pipe); tests replace it.
var stdinIsTerminal = lifecycle.StdinIsTerminal

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

// Why confirmAuthModeChange said no.
const (
	authModeApproved = iota
	authModeNotATerminal
	authModeDeclined
)

// confirmAuthModeChange asks before changing how Claude Code authenticates.
// question is the one-line ask, detail says what changes. assumeYes (--yes)
// skips the prompt but still prints the notice, so a scripted run leaves the
// same trace in the output.
//
// Stdin that is not a terminal cannot answer, so it is refused at once,
// without reading, rather than silently approved or left waiting on a pipe:
// this is exactly the operation that must not happen by accident. On a
// terminal only "y" approves; anything else, end of input included, declines.
func confirmAuthModeChange(out io.Writer, question, detail string, assumeYes bool) int {
	return confirmAuthModeChangeWith(out, question, detail, restartNotice(), assumeYes)
}

// confirmAuthModeChangeWith is confirmAuthModeChange with the sentence about
// running sessions given: a switch onto an account with a base URL says what
// such a session does with settings.json instead (DESIGN A46).
func confirmAuthModeChangeWith(out io.Writer, question, detail, notice string, assumeYes bool) int {
	fmt.Fprintln(out, detail)
	fmt.Fprintln(out, notice)
	if assumeYes {
		return authModeApproved
	}
	if !stdinIsTerminal() {
		return authModeNotATerminal
	}
	answer, ok := lifecycle.ActivePrompter.Prompt(question + " [y/N] ")
	if !ok || !strings.EqualFold(strings.TrimSpace(answer), "y") {
		return authModeDeclined
	}
	return authModeApproved
}

// confirmSwitchToAPIKey gates `switch <num|email>` onto an API-key account.
// A non-nil error is a refusal (a ValidationError, exit 1, like the switch
// layer's own refusal), and the caller must not switch. Anything it cannot
// resolve (an unknown identifier, a store error) is left to the switch itself
// to report, so error messages stay in one place. The account already in use
// is not asked about (unless force): the switch reports it as already active.
//
// --json is never prompted, since a machine-readable run has no one to ask:
// --yes approves it, as everywhere else, and without --yes the switch layer
// refuses the target with its approval error. Without --json, stdin that is
// not a terminal is refused at once and the error names --yes, the flag that
// exists to answer it.
func confirmSwitchToAPIKey(out io.Writer, identifier string, sw *core.Switcher, jsonOut, assumeYes, force bool) error {
	num, _, _, err := sw.Store.ResolveAccount(identifier)
	if err != nil || num == "" || sw.Store.AccountKindFor(num) != "api_key" {
		return nil
	}
	if !force && !assumeYes && activeSlot(sw) == num {
		return nil
	}
	if jsonOut {
		if assumeYes {
			switching.ApproveAPIKeySwitch(num)
		}
		return nil
	}
	detail := "An API-key account authenticates with a key instead of a subscription login, and its usage is billed per token."
	notice := restartNotice()
	if base := sw.Store.AccountBaseURL(num); base != "" {
		// Where the requests go is the part of this switch worth reading
		// twice, so the prompt names the whole URL; and a running session
		// takes it up when it re-reads settings.json, not only after a
		// restart (DESIGN A46).
		detail += "\n" + switching.EndpointNotice(termsafe.Strip(base))
		notice = switching.EndpointSessionNotice(runningSessions())
	}
	switch confirmAuthModeChangeWith(out,
		"Switch to API-key account #"+num+"?",
		detail,
		notice,
		assumeYes) {
	case authModeApproved:
		switching.ApproveAPIKeySwitch(num)
		return nil
	case authModeNotATerminal:
		return cerr.Validation("Not a terminal — rerun with --yes to confirm the switch to API-key account #%s.", num)
	default:
		return cerr.Validation("Cancelled: not switched to API-key account #%s.", num)
	}
}

// activeSlot is the slot the live login belongs to, or "".
func activeSlot(sw *core.Switcher) string {
	email, orgUUID, ok := sw.Store.GetCurrentAccount()
	if !ok {
		return ""
	}
	data, _ := sw.Store.ReadSequence()
	if data == nil {
		return ""
	}
	return sw.Store.FindAccountSlot(data, email, orgUUID)
}
