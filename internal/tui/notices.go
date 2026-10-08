// notices.go (FINDING 6): under the alt-screen oauth.Output goes to a collector, not io.Discard: the persist-failure warning is
// the only surface for a lost refresh token (04§1.25). The Update goroutine drains it into warning toasts.

package tui

import (
	"regexp"
	"strings"
	"sync"
)

// ansiSeq matches the SGR escape sequences printer.Yellowed wraps the warning
// in when colour is enabled, so a collected line renders cleanly inside a toast
// (which applies its own severity colour).
var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

type noticeCollector struct {
	mu    sync.Mutex
	lines []string
}

// Write records each non-empty line in p. fmt.Fprintln delivers one warning per
// Write call, but splitting on newlines keeps a multi-line write intact too.
func (c *noticeCollector) Write(p []byte) (int, error) {
	n := len(p)
	for _, raw := range strings.Split(string(p), "\n") {
		line := strings.TrimRight(ansiSeq.ReplaceAllString(raw, ""), "\r")
		if line == "" {
			continue
		}
		c.mu.Lock()
		c.lines = append(c.lines, line)
		c.mu.Unlock()
	}
	return n, nil
}

func (c *noticeCollector) drain() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lines) == 0 {
		return nil
	}
	out := c.lines
	c.lines = nil
	return out
}
