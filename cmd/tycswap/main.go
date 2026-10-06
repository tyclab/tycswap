// Command tycswap is the multi-account switcher for Claude Code (also installed
// as claude-swap). It is a thin shell around internal/cli.Main.
//
// Implements spec 08§1 (the two console-script entry points → cli.main) and
// 10-audit Gap 2 (no `python -m` equivalent; only the binary names matter).
package main

import (
	"os"

	"github.com/tyclab/tycswap/internal/cli"
	"github.com/tyclab/tycswap/internal/netproxy"
)

func main() {
	netproxy.Install()
	os.Exit(cli.Main())
}
