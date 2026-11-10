//go:build !windows

package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
)

// TestUnmigratedStoreUntouchedByHelpVersionAndARefusedRoot: the seed runs
// only once the store-root guard has passed, so a bare --help or --version
// (which touch no store) and a refused store root leave an unmigrated store
// byte for byte. Not on Windows, which refuses no store root.
func TestUnmigratedStoreUntouchedByHelpVersionAndARefusedRoot(t *testing.T) {
	const prefs = `{"version":1,"autoSwitch":true}`
	for _, tc := range []struct {
		name   string
		argv   []string
		refuse bool
	}{
		{"help", []string{"--help"}, false},
		{"version", []string{"--version"}, false},
		{"refused root", []string{"config", "get", "autoswitch.enabled"}, true},
	} {
		// "" is a store without settings.json; the other has one without the key.
		for _, settingsBody := range []string{"", `{"schemaVersion":1,"autoswitch":{"fiveHourThreshold":80}}`} {
			t.Run(tc.name, func(t *testing.T) {
				cleanHome(t)
				root := paths.GetBackupRoot()
				writeTestFile(t, appStatePath(root), prefs, 0o600)
				if settingsBody != "" {
					writeTestFile(t, settings.SettingsPath(root), settingsBody, 0o600)
				}
				if tc.refuse {
					prev := paths.PrivateRootStat
					t.Cleanup(func() { paths.PrivateRootStat = prev })
					paths.PrivateRootStat = func(string) (os.FileInfo, error) { return nil, errors.New("permission denied") }
				}
				var out, errb bytes.Buffer
				code := run("tycswap", tc.argv, ioStreams{in: strings.NewReader(""), out: &out, err: &errb}, false, false)
				if tc.refuse && (code != 1 || !strings.Contains(errb.String(), "cannot check the tycswap store")) {
					t.Fatalf("exit %d, stderr %q; want the root refused", code, errb.String())
				}
				if got, _ := os.ReadFile(appStatePath(root)); string(got) != prefs {
					t.Errorf("ui_prefs.json = %s, want it untouched", got)
				}
				got, err := os.ReadFile(settings.SettingsPath(root))
				switch {
				case settingsBody == "" && !os.IsNotExist(err):
					t.Errorf("settings.json created: %s (%v)", got, err)
				case settingsBody != "" && string(got) != settingsBody:
					t.Errorf("settings.json = %s, want it untouched", got)
				}
			})
		}
	}
}
