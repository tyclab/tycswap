package cli

import (
	"strings"
	"testing"
)

// `tycswap web` ends with 0 when Ctrl-C or SIGTERM stops it (the notify-context
// seam), as `tycswap app` does: a stop it was asked for is not a failure
// (DESIGN A48). It used to end with 130 on either.
func TestWebEndsWithZeroWhenStopped(t *testing.T) {
	appTestHome(t)
	prev := webOptions
	webOptions = dashboardOptions{noUpdateSchedule: true}
	t.Cleanup(func() { webOptions = prev })
	stop := stopViaNotifyContext(t)
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run("tycswap", []string{"web", "--no-open", "--port", "0"}, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
	}()
	waitUntil(t, "the Dashboard line", func() bool {
		return strings.Contains(errb.String(), "Dashboard: http://127.0.0.1:")
	})
	stop()
	if code := waitExit(t, done, &errb); code != 0 {
		t.Errorf("exit = %d, stderr = %q", code, errb.String())
	}
}
