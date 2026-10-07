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

func launchers(goos string) []string {
	if goos == "darwin" {
		return []string{"open"}
	}
	return []string{"wslview", "xdg-open", "sensible-browser"}
}

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
