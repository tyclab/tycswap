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

// A detection failure counts as zero: the notice is advisory.
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

// confirmAuthModeChange asks before changing how Claude Code authenticates; assumeYes skips the prompt but still prints the notice.
// Non-terminal stdin is refused at once, never approved or left waiting: this must not happen by accident. Only "y" approves.
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

// confirmSwitchToAPIKey gates a switch onto an API-key account; a non-nil error is a refusal and the caller must not switch.
// Unresolvable identifiers are left to the switch to report; the active account is not asked about unless force.
// --json never prompts: --yes approves, else the switch layer refuses; non-terminal stdin is refused naming --yes.
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
