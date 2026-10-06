//go:build darwin

package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readSystem reads the active network service's proxy, where a configuration
// profile's proxy lands too. The absolute path keeps a scutil planted earlier
// in PATH from running.
func readSystem() settings {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return settings{}
	}
	return parseScutil(string(out))
}
