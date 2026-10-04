//go:build windows

package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A console shared with a shell, or no console at all, is left as it is: that
// is `tycswap app` typed into a terminal, or the detached background
// start (A40/A41).
func TestReleaseOwnConsoleLeavesSharedConsoles(t *testing.T) {
	prev := consoleProcessCount
	t.Cleanup(func() { consoleProcessCount = prev })
	var out bytes.Buffer
	s := ioStreams{in: strings.NewReader(""), out: &out, err: &out}
	for _, n := range []int{0, 2, 3} {
		consoleProcessCount = func() int { return n }
		if got := releaseOwnConsole(s); got.out != s.out || got.err != s.err {
			t.Errorf("count %d: streams replaced", n)
		}
	}
}
