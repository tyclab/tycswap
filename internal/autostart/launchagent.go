package autostart

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/brand"
)

// LogPath is the file a LaunchAgent started app writes to,
// ~/Library/Logs/<name>.log; the background start appends to the same file,
// so both ways of starting the app on macOS share one log.
func LogPath(home string) string {
	return filepath.Join(home, "Library", "Logs", brand.Sanitized().Name+".log")
}

// launchAgentPath is ~/Library/LaunchAgents/<Label>.plist.
func launchAgentPath(c Config) string {
	return filepath.Join(c.Home, "Library", "LaunchAgents", c.Label+".plist")
}

// launchAgentPlist renders the agent: run at login, do not respawn on exit
// (Quit in the tray must stick), logs under ~/Library/Logs.
func launchAgentPlist(c Config) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + c.Label + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range append([]string{c.Exe}, c.Args...) {
		b.WriteString("\t\t<string>")
		_ = xml.EscapeText(&b, []byte(a))
		b.WriteString("</string>\n")
	}
	log := LogPath(c.Home)
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>`)
	_ = xml.EscapeText(&b, []byte(log))
	b.WriteString(`</string>
	<key>StandardErrorPath</key>
	<string>`)
	_ = xml.EscapeText(&b, []byte(log))
	b.WriteString(`</string>
</dict>
</plist>
`)
	return b.String()
}

func launchAgentEnable(c Config) error {
	p := launchAgentPath(c)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(launchAgentPlist(c)), 0o644); err != nil {
		return err
	}
	// Load it now so the icon appears without a re-login. `bootstrap` is the
	// modern verb; `load -w` the pre-10.11 one still accepted everywhere.
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = c.Run("launchctl", "bootout", domain+"/"+c.Label) // replace a stale registration
	if err := c.Run("launchctl", "bootstrap", domain, p); err != nil {
		return c.Run("launchctl", "load", "-w", p)
	}
	return nil
}

func launchAgentDisable(c Config) error {
	p := launchAgentPath(c)
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = c.Run("launchctl", "bootout", domain+"/"+c.Label)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
