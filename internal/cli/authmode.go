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

var stdinIsTerminal = lifecycle.StdinIsTerminal

var runningSessions = func() int {
	return len(procdetect.ListSessions(procdetect.GetClaudeDir()))
}

func restartNotice() string {
	return switching.RestartNotice(runningSessions())
}

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
