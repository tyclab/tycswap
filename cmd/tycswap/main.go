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
