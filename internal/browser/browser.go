// Package browser opens a URL in the user's default browser.
//
// `open` on macOS and a wslview → xdg-open → sensible-browser chain on other
// unix systems (wslview first, so a WSL distro reaches the Windows browser).
// On Windows the shell is asked directly (ShellExecuteW), then
// `rundll32 url.dll,FileProtocolHandler`, and only last `cmd /c start "" <url>`
// (with & escaped as ^& so cmd does not split the query). The child is
// detached with stdio ignored, so a chatty browser never writes into the
// caller's output.
package browser

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Open launches url in the user's default browser without
// waiting for it. It refuses URLs containing characters outside the RFC 3986
// set, since the unix path interpolates the URL into an `sh -c` string.
//
// On Windows it asks the shell itself (ShellExecuteW) first and only then
// spawns a launcher (openURL in browser_windows.go): a command line through
// cmd.exe has its own quoting and expansion rules and flashes a console
// window from a tray app.
func Open(url string) error {
	if !urlShellSafe(url) {
		return errors.New("refusing to open a URL with shell-significant characters")
	}
	return openURL(url)
}

// startLauncher runs the platform's launcher command for url, detached, and
// reaps it in the background so it never lingers as a zombie.
func startLauncher(goos, url string) error {
	name, args := browserCommand(goos, url)
	cmd := exec.Command(name, args...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
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

// browserCommand picks the launcher argv for a GOOS; split out so every
// platform branch is unit-tested on every platform.
func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "cmd", []string{"/c", "start", "", strings.ReplaceAll(url, "&", "^&")}
	default:
		q := `"` + url + `"`
		script := fmt.Sprintf("wslview %s 2>/dev/null || xdg-open %s 2>/dev/null || sensible-browser %s 2>/dev/null", q, q, q)
		return "sh", []string{"-c", script}
	}
}

// urlShellSafe reports whether every byte of url is in the RFC 3986 unreserved
// / reserved / percent set, less the characters that are shell-significant
// inside double quotes. A url.Values-encoded query always passes.
func urlShellSafe(url string) bool {
	if url == "" {
		return false
	}
	for i := 0; i < len(url); i++ {
		c := url[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-._~:/?#[]@!$&'()*+,;=%", c) >= 0:
			// '$', '\'' and '!' are RFC-legal but shell-significant inside
			// double quotes (sh) — url.Values never emits them, so reject.
			if c == '$' || c == '\'' || c == '!' || c == '`' {
				return false
			}
		default:
			return false
		}
	}
	return true
}
