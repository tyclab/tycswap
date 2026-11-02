package cli

import (
	"fmt"

	"github.com/tyclab/tycswap/internal/session"
)

func claudeCommand(prog string, args []string, streams ioStreams) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintf(streams.out, "usage: %s claude [--model MODEL] [--account ACCOUNT] [--resume ID|PATH] [Claude arguments]\n", prog)
		fmt.Fprintln(streams.out, "Selects accounts automatically for model requests while preserving parallel credential profiles.")
		return 0
	}
	sw, err := constructSwitcher(false, streams.err)
	if err != nil {
		return renderDomainError(err, false, streams.out, streams.err)
	}
	if code, blocked := guardRoot(streams.err); blocked {
		return code
	}
	manager := session.NewManager(sw, session.Options{OAuth: sw.OAuth, Keychain: sw.Keychain(), Clock: sw.Clk, Logger: sw.Log, Stdout: streams.err})
	if err := manager.RunManaged("", "", "", "", args); err != nil {
		errorTo(streams.err, "Error: "+err.Error())
		return 1
	}
	return 0
}
