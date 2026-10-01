// Package browser opens a URL in the user's default browser.
//
// `open` on macOS and a wslview → xdg-open → sensible-browser chain on other
// unix systems (wslview first, so a WSL distro reaches the Windows browser),
// each launcher looked up on PATH and started directly: no shell is involved,
// and success means a launcher process actually started. On Windows the shell
// is asked directly (ShellExecuteW), then `rundll32 url.dll,FileProtocolHandler`,
// and only last `cmd /c start "" <url>` (with & escaped as ^& so cmd does not
// split the query). The child is detached with stdio ignored, so a chatty
// browser never writes into the caller's output.
package browser

import (
	"errors"
	"os/exec"
	"strings"
)

// Open launches url in the user's default browser without waiting for it.
// It refuses URLs containing characters outside the RFC 3986 set or that a
// command interpreter treats specially: the Windows `cmd /c start` fallback
// re-parses its command line, and refusing them everywhere keeps one rule.
func Open(url string) error {
	if !urlShellSafe(url) {
		return errors.New("refusing to open a URL with shell-significant characters")
	}
	return openURL(url)
}

// opener is one way of handing a URL to the default browser.
type opener struct {
	name string
	open func(url string) error
}

// openChain tries each opener in order and stops at the first that works.
// When every one fails, the error names each attempt and why, so a user (or
// a log line) sees more than the last fallback's complaint.
func openChain(url string, steps []opener) error {
	var why []string
	for _, s := range steps {
		err := s.open(url)
		if err == nil {
			return nil
		}
		why = append(why, s.name+": "+err.Error())
	}
	if len(why) == 0 {
		return errors.New("no way to open a browser on this system")
	}
	return errors.New("could not open a browser (" + strings.Join(why, "; ") + ")")
}

// launchers names the unix launcher binaries for a GOOS, in the order they
// are tried; split out so every platform's chain is unit-tested on every
// platform. Windows has its own chain (browser_windows.go).
func launchers(goos string) []string {
	if goos == "darwin" {
		return []string{"open"}
	}
	return []string{"wslview", "xdg-open", "sensible-browser"}
}

// launcherOpeners builds the opener chain for the unix launchers: each one is
// resolved on PATH (a missing binary is that step's failure, and the chain
// moves on) and started detached with the URL as its only argument.
func launcherOpeners(goos string) []opener {
	var steps []opener
	for _, bin := range launchers(goos) {
		bin := bin
		steps = append(steps, opener{name: bin, open: func(url string) error {
			path, err := exec.LookPath(bin)
			if err != nil {
				return err
			}
			return startDetached(path, url)
		}})
	}
	return steps
}

// startDetached runs the launcher at path for url, detached, and reaps it in
// the background so it never lingers as a zombie. nil means the launcher
// process started.
func startDetached(path, url string) error {
	cmd := exec.Command(path, url)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// cmdStartArgs is the argv for `cmd.exe` opening url with `start`: & escaped
// as ^& so cmd does not split the query string.
func cmdStartArgs(url string) []string {
	return []string{"/c", "start", "", strings.ReplaceAll(url, "&", "^&")}
}

// urlShellSafe reports whether every byte of url is in the RFC 3986 unreserved
// / reserved / percent set, less the characters a command interpreter treats
// specially. A url.Values-encoded query always passes.
func urlShellSafe(url string) bool {
	if url == "" {
		return false
	}
	for i := 0; i < len(url); i++ {
		c := url[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-._~:/?#[]@!$&'()*+,;=%", c) >= 0:
			// '$', '\'', '!' and '`' are RFC-legal but shell-significant —
			// url.Values never emits them, so reject.
			if c == '$' || c == '\'' || c == '!' || c == '`' {
				return false
			}
		default:
			return false
		}
	}
	return true
}
