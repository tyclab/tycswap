package cli

import (
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

const groupModelReadOnly = "Model limits follow this group's live sessions."

func groupSettingSupported(key string) bool {
	switch key {
	case "autoswitch.fiveHourThreshold", "autoswitch.sevenDayThreshold", "autoswitch.model", "autoswitch.modelThreshold",
		"autoswitch.cooldownSeconds", "autoswitch.hysteresisPct", "autoswitch.strategy":
		return true
	}
	return false
}

func (f groupFacade) settingScope(group string) (*core.Switcher, error) {
	id, err := groups.Parse(group)
	if err != nil {
		return nil, cerr.Validation("%s", err)
	}
	return f.sw.ForGroup(id)
}

func (f groupFacade) Settings(group string) ([]web.SettingView, error) {
	scope, err := f.settingScope(group)
	if err != nil {
		return nil, err
	}
	base := settings.Load(f.sw.BackupDir())
	inherited := settings.ValuesOf(base)
	effective := settings.ValuesOf(groupSettings(scope, base))
	views := (settingsFacade{root: scope.ScopeRoot()}).Effective()
	items := make([]web.SettingView, 0, 7)
	for _, view := range views {
		if !groupSettingSupported(view.Key) {
			continue
		}
		view.Scope = string(scope.GroupID())
		view.Source = "group"
		if view.IsDefault {
			view.Source = "default"
		}
		view.Default = inherited[view.Key]
		view.Value = effective[view.Key]
		view.Applies = "At the next group rotation check. The shared scheduler controls when checks run."
		if view.Key == "autoswitch.model" {
			view.Source = "session"
			view.ReadOnly = groupModelReadOnly
			view.Description = groupModelReadOnly
			view.Default = nil
			view.IsDefault = true
			view.Applies = "Follows confirmed session models. Unknown or incompatible models hold group rotation."
		}
		items = append(items, view)
	}
	return items, nil
}

func (f groupFacade) editableSettingScope(group, key string) (*core.Switcher, error) {
	scope, err := f.settingScope(group)
	if err != nil {
		return nil, err
	}
	if key == "autoswitch.model" {
		return nil, cerr.Validation("%s", groupModelReadOnly)
	}
	if !groupSettingSupported(key) {
		return nil, cerr.Validation("%s is not a group setting; use Default settings for shared controls", key)
	}
	return scope, nil
}

func (f groupFacade) SetSetting(group, key, raw string) (any, error) {
	scope, err := f.editableSettingScope(group, key)
	if err != nil {
		return nil, err
	}
	return (settingsFacade{root: scope.ScopeRoot()}).Set(key, raw)
}

func (f groupFacade) UnsetSetting(group, key string) (bool, error) {
	scope, err := f.editableSettingScope(group, key)
	if err != nil {
		return false, err
	}
	return (settingsFacade{root: scope.ScopeRoot()}).Unset(key)
}

var _ web.GroupSettingsFacade = groupFacade{}
