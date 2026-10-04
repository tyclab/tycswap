package autostart

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/brand"
)

// xdgPath is $XDG_CONFIG_HOME/autostart/<Label>.desktop, defaulting to
// ~/.config (the Desktop Application Autostart Specification).
func xdgPath(c Config) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || !filepath.IsAbs(base) {
		base = filepath.Join(c.Home, ".config")
	}
	return filepath.Join(base, "autostart", c.Label+".desktop")
}

func xdgDesktopEntry(c Config) string {
	exec := make([]string, 0, 1+len(c.Args))
	for _, a := range append([]string{c.Exe}, c.Args...) {
		exec = append(exec, desktopQuote(a))
	}
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + brand.Sanitized().DisplayName + "\n" +
		"Comment=Claude account switcher\n" +
		"Exec=" + strings.Join(exec, " ") + "\n" +
		"Icon=utilities-terminal\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n" +
		"StartupNotify=false\n"
}

// desktopQuote follows the Desktop Entry Exec quoting rules: quote when the
// argument has reserved characters, escaping backslash and double quote.
func desktopQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(s) + `"`
}

func xdgEnable(c Config) error {
	p := xdgPath(c)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(xdgDesktopEntry(c)), 0o644)
}

func xdgDisable(c Config) error {
	if err := os.Remove(xdgPath(c)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
