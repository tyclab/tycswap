// Command tycswap is the multi-account switcher for Claude Code and Codex (also installed as claude-swap).
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
