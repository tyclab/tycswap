package cli

import (
	"testing"

	"github.com/tyclab/tycswap/internal/web"
)

func TestRemoteTrayEditsOneGroupAndResetsToSharedDefault(t *testing.T) {
	sw := fixtureSwitcher(t)
	groups := groupFacade{sw}
	fx := startRemoteServer(t, testRemoteToken, "127.0.0.1:0", func(d *web.Deps) {
		d.Groups = groups
		d.Settings = settingsFacade{root: sw.BackupDir()}
	})
	rc := newRemoteClient(fx.base, tokenFile(t, testRemoteToken))
	if err := rc.loadToken(); err != nil {
		t.Fatal(err)
	}
	value := "89"
	key := "autoswitch.fiveHourThreshold"
	if err := rc.SaveSetting("fable", key, &value); err != nil {
		t.Fatal(err)
	}
	if fable := groupSetting(t, groups, "fable", key); fable.Value != float64(89) || fable.IsDefault {
		t.Fatalf("remote group override not saved: %+v", fable)
	}
	if opus := groupSetting(t, groups, "opus", key); opus.Value != float64(85) || !opus.IsDefault {
		t.Fatalf("remote group edit escaped its scope: %+v", opus)
	}
	if err := rc.SaveSetting("fable", key, nil); err != nil {
		t.Fatal(err)
	}
	if fable := groupSetting(t, groups, "fable", key); fable.Value != float64(85) || !fable.IsDefault {
		t.Fatalf("remote reset did not restore shared default: %+v", fable)
	}
	if err := rc.SaveSetting("fable", "autoswitch.model", &value); err == nil {
		t.Fatal("remote tray changed a group's model policy")
	}
	for _, scope := range []string{"default", "Default", " ", "unknown"} {
		if err := rc.SaveSetting(scope, key, &value); err == nil {
			t.Fatalf("remote tray accepted invalid group scope %q", scope)
		}
	}
	if got := (settingsFacade{root: sw.BackupDir()}).Effective(); len(got) == 0 {
		t.Fatal("shared settings disappeared")
	} else {
		for _, setting := range got {
			if setting.Key == key && setting.Value != float64(85) {
				t.Fatal("invalid group scope changed shared settings")
			}
		}
	}
}
