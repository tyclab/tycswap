package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recorder struct{ calls [][]string }

func (r *recorder) run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil
}

func TestLaunchAgentEnableDisable(t *testing.T) {
	home := t.TempDir()
	rec := &recorder{}
	c := Config{Home: home, Exe: "/Users/me/go/bin/tycswap", Run: rec.run, GOOS: "darwin"}

	if err := Enable(c); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<key>Label</key>", "<string>" + Label + "</string>", "<string>/Users/me/go/bin/tycswap</string>", "<string>app</string>", "<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<false/>", LogPath(home)} {
		if !strings.Contains(string(body), want) {
			t.Errorf("plist lacks %q:\n%s", want, body)
		}
	}
	if on, _ := Enabled(c); !on {
		t.Error("Enabled = false after Enable")
	}
	// launchctl was asked to (re)load it: bootout then bootstrap.
	if len(rec.calls) < 2 || rec.calls[0][1] != "bootout" || rec.calls[1][1] != "bootstrap" || rec.calls[1][3] != p {
		t.Errorf("launchctl calls = %v", rec.calls)
	}

	if err := Disable(c); err != nil {
		t.Fatal(err)
	}
	if on, _ := Enabled(c); on {
		t.Error("Enabled = true after Disable")
	}
	if err := Disable(c); err != nil {
		t.Errorf("second Disable should be a no-op, got %v", err)
	}
}

func TestLaunchAgentEscapesXML(t *testing.T) {
	c := Config{Home: "/h", Exe: `/Apps/a & b/tycswap`, Args: []string{"app", "--token-file", "/tmp/<1>/remote.token"}}
	got := launchAgentPlist(c)
	if !strings.Contains(got, "a &amp; b") || !strings.Contains(got, "&lt;1&gt;") {
		t.Errorf("unescaped XML in plist:\n%s", got)
	}
}

func TestXDGEnableDisable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	c := Config{Home: home, Exe: "/home/me/go/bin/tycswap", GOOS: "linux", Run: (&recorder{}).run}

	if err := Enable(c); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".config", "autostart", Label+".desktop")
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Desktop Entry]", "Type=Application", "Exec=/home/me/go/bin/tycswap app", "X-GNOME-Autostart-enabled=true"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("desktop entry lacks %q:\n%s", want, body)
		}
	}
	if on, _ := Enabled(c); !on {
		t.Error("Enabled = false after Enable")
	}
	if err := Disable(c); err != nil {
		t.Fatal(err)
	}
	if on, _ := Enabled(c); on {
		t.Error("Enabled = true after Disable")
	}
}

func TestXDGHonoursConfigHomeAndQuotes(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	c := Config{Home: "/ignored", Exe: `/opt/my tools/tycswap`, Args: []string{"app", `--token-file`, `/tmp/a?b/remote.token`}, GOOS: "linux"}
	if err := Enable(c); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(cfg, "autostart", Label+".desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `Exec="/opt/my tools/tycswap" app --token-file "/tmp/a?b/remote.token"`) {
		t.Errorf("Exec quoting wrong:\n%s", body)
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	c := Config{Home: t.TempDir(), Exe: "/x", GOOS: "plan9"}
	if err := Enable(c); err != ErrUnsupported {
		t.Errorf("Enable = %v", err)
	}
	if err := Disable(c); err != ErrUnsupported {
		t.Errorf("Disable = %v", err)
	}
	if _, err := Enabled(c); err != ErrUnsupported {
		t.Errorf("Enabled = %v", err)
	}
}

// A remote tray's entry carries its flags on every platform (DESIGN A45):
// the Run value, the LaunchAgent and the desktop entry all render
// `app --remote URL --token-file PATH`, a UNC path with a space quoted the
// way each reader expects.
func TestRemoteArgsRenderedOnEveryPlatform(t *testing.T) {
	const url = "http://127.0.0.1:7337"
	unc := `\\wsl.localhost\Ubuntu\home\me\.local\share\tycswap\remote.token`
	spaced := `\\wsl.localhost\Ubuntu-24 LTS\home\me\remote.token`

	c := Config{Home: "/h", Exe: `C:\Users\me\go\bin\tycswap.exe`, Args: []string{"app", "--remote", url, "--token-file", unc}}
	if got, want := registryCommand(c), `"C:\Users\me\go\bin\tycswap.exe" app --remote http://127.0.0.1:7337 --token-file `+unc; got != want {
		t.Errorf("Run value = %s, want %s", got, want)
	}
	c.Args[4] = spaced
	if got, want := registryCommand(c), `"C:\Users\me\go\bin\tycswap.exe" app --remote http://127.0.0.1:7337 --token-file "`+spaced+`"`; got != want {
		t.Errorf("Run value with a space = %s, want %s", got, want)
	}

	c = Config{Home: "/Users/me", Exe: "/usr/local/bin/tycswap", Args: []string{"app", "--remote", url, "--token-file", "/Volumes/wsl/remote.token"}}
	plist := launchAgentPlist(c)
	for _, want := range []string{"<string>app</string>", "<string>--remote</string>", "<string>" + url + "</string>", "<string>--token-file</string>", "<string>/Volumes/wsl/remote.token</string>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}

	c = Config{Home: "/home/me", Exe: "/usr/local/bin/tycswap", Args: []string{"app", "--remote", url, "--token-file", "/mnt/wsl/my distro/remote.token"}}
	if entry, want := xdgDesktopEntry(c), `Exec=/usr/local/bin/tycswap app --remote http://127.0.0.1:7337 --token-file "/mnt/wsl/my distro/remote.token"`; !strings.Contains(entry, want) {
		t.Errorf("desktop entry lacks %q:\n%s", want, entry)
	}
}

func TestLabelNamesEveryEntry(t *testing.T) {
	c := Config{Home: "/h", Exe: "/x", Label: Label + ".remote"}.withDefaults()
	if got := filepath.Base(xdgPath(c)); got != "io.github.tyclab.tycswap.remote.desktop" {
		t.Errorf("desktop entry = %q", got)
	}
	if got := filepath.Base(launchAgentPath(c)); got != "io.github.tyclab.tycswap.remote.plist" {
		t.Errorf("launch agent = %q", got)
	}
	if !strings.Contains(launchAgentPlist(c), "<string>io.github.tyclab.tycswap.remote</string>") {
		t.Error("plist label is not the entry's own")
	}
	if d := (Config{Home: "/h", Exe: "/x"}).withDefaults(); d.Label != Label {
		t.Errorf("default label = %q", d.Label)
	}
}

// The desktop entry names the program and says what it is, in the build's
// brand, and the LaunchAgent's log is the build's own.
func TestEntriesCarryTheBrand(t *testing.T) {
	c := Config{Home: "/home/me", Exe: "/x", GOOS: "linux"}.withDefaults()
	entry := xdgDesktopEntry(c)
	for _, want := range []string{"Name=tycswap\n", "Comment=Claude account switcher\n"} {
		if !strings.Contains(entry, want) {
			t.Errorf("desktop entry lacks %q:\n%s", want, entry)
		}
	}
	if got := LogPath("/Users/me"); got != filepath.Join("/Users/me", "Library", "Logs", "tycswap.log") {
		t.Errorf("LogPath = %q", got)
	}
}

// TestLaunchctlRefusesInTests: the default Run is the real launchctl, which
// in a test binary refuses before starting anything.
func TestLaunchctlRefusesInTests(t *testing.T) {
	err := Config{}.withDefaults().Run("launchctl", "version")
	if err == nil || !strings.Contains(err.Error(), "not reachable from tests") {
		t.Fatalf("Run = %v, want the refusal", err)
	}
}
