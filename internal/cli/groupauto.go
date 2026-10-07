package cli

import (
	"fmt"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
)

type groupEvent struct {
	autoswitch.Event
	group groups.ID
}

func (e groupEvent) JSON() map[string]any {
	v := e.Event.JSON()
	v["group"] = string(e.group)
	return v
}
func (e groupEvent) Human() string { return "[" + e.group.Label() + "] " + e.Event.Human() }

func groupSettings(sw *core.Switcher, base settings.AutoSwitchSettings) settings.AutoSwitchSettings {
	return reporting.GroupSettings(sw.Store, base)
}

func groupModelBlocker(sw *core.Switcher, observed recovery.State) string {
	sessions, _, err := procdetect.GetRunningInstancesErr(sw.ProfileDir())
	if err != nil {
		return "cannot verify session models"
	}
	for _, session := range sessions {
		event, ok := observed.Sessions[session.SessionID]
		if !ok || event.Event.Group != string(sw.GroupID()) {
			return "session model is unknown; restart through the group launcher"
		}
		if err := groups.CheckModel(sw.GroupID(), event.Event.Model); err != nil {
			return err.Error()
		}
	}
	return ""
}

func tickGroupScopes(root *core.Switcher, base settings.AutoSwitchSettings, emit func(autoswitch.Event), dry bool) {
	statuses, err := root.GroupStatuses()
	if err != nil {
		root.Log.Warningf("session groups: %v", err)
		return
	}
	observed, recordErr := recovery.NewStore(root.BackupDir()).Snapshot()
	type running struct {
		sw     *core.Switcher
		engine *autoswitch.Engine
	}
	var engines []running
	for _, status := range statuses {
		if !status.Enabled || status.LiveSessions == 0 {
			reporting.ClearScopedPollInputs(root.BackupDir(), groups.ScopeDir(root.BackupDir(), status.ID))
			continue
		}
		sw, err := root.ForGroup(status.ID)
		if err != nil {
			continue
		}
		blocker := status.Blocker
		if recordErr != nil {
			blocker = "session model record is unreadable"
		}
		if blocker == "" {
			blocker = groupModelBlocker(sw, observed)
		}
		if blocker != "" {
			sw.ClearPollPolicyInputs()
			root.Log.Warningf("%s rotation held: %s", status.Label, blocker)
			continue
		}
		id := status.ID
		onEvent := func(ev autoswitch.Event) {
			if emit != nil {
				emit(groupEvent{ev, id})
			}
		}
		engine := newAutoEngine(sw, groupSettings(sw, base), onEvent, dry, sw.OAuth)
		engines = append(engines, running{sw, engine})
	}
	for _, run := range engines {
		run.engine.Tick()
	}
}

func (f groupFacade) Reconcile(id string) error {
	g, err := groups.Parse(id)
	if err != nil {
		return err
	}
	sw, err := f.sw.ForGroup(g)
	if err != nil {
		return err
	}
	if err := sw.ReconcileGroup(); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	return nil
}
