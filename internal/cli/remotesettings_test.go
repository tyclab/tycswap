package cli

import (
	"testing"

	"github.com/tyclab/tycswap/internal/web"
)

// The remote tray edits the one settings scope left after session groups
// retired; a group scope is refused before it reaches the dashboard (A78).
func TestRemoteTrayEditsSharedSettingsOnly(t *testing.T) {
	sw := fixtureSwitcher(t)
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0", func(d *web.Deps) {
		d.Settings = settingsFacade{root: sw.BackupDir()}
	})
	rc := newRemoteClient(fx.base, tokenFile(t, testRemoteToken))
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	value := "89"
	key := "autoswitch.fiveHourThreshold"
	effective := func() any {
		for _, setting := range (settingsFacade{root: sw.BackupDir()}).Effective() {
			if setting.Key == key {
				return setting.Value
			}
		}
		return nil
	}
	if err := rc.SaveSetting("fable", key, &value); err == nil {
		t.Fatal("remote tray saved a group-scoped setting")
	}
	if got := effective(); got != float64(85) {
		t.Fatalf("a refused scope changed shared settings: %v", got)
	}
	if err := rc.SaveSetting("", key, &value); err != nil {
		t.Fatal(err)
	}
	if got := effective(); got != float64(89) {
		t.Fatalf("shared setting not saved: %v", got)
	}
	if err := rc.SaveSetting("", key, nil); err != nil {
		t.Fatal(err)
	}
	if got := effective(); got != float64(85) {
		t.Fatalf("shared reset did not restore the default: %v", got)
	}
}
