package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

func isGroupSettingValidation(err error) bool {
	var domain *cerr.Error
	return errors.As(err, &domain) && domain.Kind == cerr.KindValidation
}

func TestGroupTargetsRequireStartCapabilityUntilSourceIsLive(t *testing.T) {
	sw := fixtureSwitcher(t)
	roster, err := sw.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	record := map[string]any{}
	if err := json.Unmarshal(roster.Accounts["2"], &record); err != nil {
		t.Fatal(err)
	}
	record["fableStart"], record["fableContinue"] = false, true
	roster.Accounts["2"], err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.WriteSequence(roster); err != nil {
		t.Fatal(err)
	}
	email, _ := record["email"].(string)
	if err := sw.WriteAccountCredentials("2", email, `{"claudeAiOauth":{"accessToken":"synthetic-business","refreshToken":"synthetic-refresh","subscriptionType":"team"}}`); err != nil {
		t.Fatal(err)
	}
	facade := groupFacade{sw}
	fable := func() web.GroupView {
		for _, view := range facade.Views() {
			if view.ID == "fable" {
				return view
			}
		}
		t.Fatal("Fable group missing")
		return web.GroupView{}
	}
	if view := fable(); view.AccountBlockers["2"] == "" {
		t.Fatal("inactive Fable offered a continuation-only account")
	}
	sessionID := "synthetic-fable-source"
	profile := groups.ProfileDir(sw.BackupDir(), groups.Fable)
	registry := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(registry, 0o700); err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": sessionID})
	if err := os.WriteFile(filepath.Join(registry, "source.json"), row, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.NewStore(sw.BackupDir()).Record(recovery.Event{Provider: "claude", SessionID: sessionID, Group: "fable", Model: "fable", Kind: "SessionStart", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if view := fable(); view.AccountBlockers["2"] != "" || view.Blocker != "" {
		t.Fatalf("live Fable rejected continuation capability: %+v", view)
	}
}

func groupSetting(t *testing.T, facade groupFacade, group, key string) web.SettingView {
	t.Helper()
	views, err := facade.Settings(group)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Key == key {
			return view
		}
	}
	t.Fatalf("missing %s in %s settings", key, group)
	return web.SettingView{}
}

func TestGroupSettingsInheritanceOverridesAndReset(t *testing.T) {
	sw := fixtureSwitcher(t)
	before, err := os.ReadFile(sw.SequenceFile)
	if err != nil {
		t.Fatal(err)
	}
	facade := groupFacade{sw}
	key := "autoswitch.sevenDayThreshold"
	if _, err := settings.SetSetting(sw.BackupDir(), key, "72"); err != nil {
		t.Fatal(err)
	}
	view := groupSetting(t, facade, "fable", key)
	if view.Value != float64(72) || view.Default != float64(72) || !view.IsDefault || view.Source != "default" || view.Scope != "fable" || view.Label != "Weekly threshold" {
		t.Fatalf("inherited view %+v", view)
	}
	if _, err := facade.SetSetting("fable", key, "88"); err != nil {
		t.Fatal(err)
	}
	view = groupSetting(t, facade, "fable", key)
	if view.Value != float64(88) || view.Default != float64(72) || view.IsDefault || view.Source != "group" {
		t.Fatalf("override view %+v", view)
	}
	if other := groupSetting(t, facade, "opus", key); other.Value != float64(72) || !other.IsDefault {
		t.Fatalf("Fable override affected Opus %+v", other)
	}
	if _, err := settings.SetSetting(sw.BackupDir(), key, "74"); err != nil {
		t.Fatal(err)
	}
	if view = groupSetting(t, facade, "fable", key); view.Value != float64(88) || view.Default != float64(74) {
		t.Fatalf("shared save displaced group override %+v", view)
	}
	removed, err := facade.UnsetSetting("fable", key)
	if err != nil || !removed {
		t.Fatalf("reset %v %v", removed, err)
	}
	if view = groupSetting(t, facade, "fable", key); view.Value != float64(74) || view.Default != float64(74) || !view.IsDefault || view.Source != "default" {
		t.Fatalf("reset did not inherit current shared value %+v", view)
	}
	if after, err := os.ReadFile(sw.SequenceFile); err != nil || string(after) != string(before) {
		t.Fatal("settings edit changed account state")
	}
}

func TestGroupSettingsEnforceModelsAndRejectUnusedControls(t *testing.T) {
	sw := fixtureSwitcher(t)
	facade := groupFacade{sw}
	model := groupSetting(t, facade, "fable", "autoswitch.model")
	if model.Value != "Fable" || model.Source != "session" || model.ReadOnly == "" || model.Applies == "" {
		t.Fatalf("model view %+v", model)
	}
	for _, key := range []string{"autoswitch.model", "autoswitch.intervalSeconds", "autoswitch.codexEnabled", "autoswitch.codexThreshold", "autoswitch.handoverWaitMinutes", "autoswitch.unhealthyTicks", "autoswitch.unknown"} {
		if _, err := facade.SetSetting("fable", key, "all"); !isGroupSettingValidation(err) {
			t.Errorf("%s save accepted or misclassified: %v", key, err)
		}
		if _, err := facade.UnsetSetting("opus", key); !isGroupSettingValidation(err) {
			t.Errorf("%s reset accepted or misclassified: %v", key, err)
		}
	}
	if _, err := facade.SetSetting("fable", "autoswitch.fiveHourThreshold", "101"); !isGroupSettingValidation(err) {
		t.Fatalf("out-of-range value accepted: %v", err)
	}
	for _, id := range groups.All() {
		views, err := facade.Settings(string(id))
		if err != nil || len(views) != 7 {
			t.Fatalf("%s settings %v %v", id, views, err)
		}
		for _, view := range views {
			if view.Label == "" || view.Label == view.Key || view.Applies == "" {
				t.Errorf("unusable shared form metadata %+v", view)
			}
		}
		if _, err := os.Stat(settings.SettingsPath(groups.ScopeDir(sw.BackupDir(), id))); !os.IsNotExist(err) {
			t.Fatalf("rejected group writes created settings: %v", err)
		}
	}
}
