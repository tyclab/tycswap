package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/switching"
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
	tickGroupScopesFresh(root, base, emit, dry, "")
}

// tickGroupScopesFresh is tickGroupScopes whose fresh group fetches its active
// account's usage ahead of the poll plan (DESIGN A75).
func tickGroupScopesFresh(root *core.Switcher, base settings.AutoSwitchSettings, emit func(autoswitch.Event), dry bool, fresh groups.ID) {
	// A dry tick resolves read-only from the root on: the statuses resolve the
	// default login too, and ForGroup keeps the root's read-only view (DESIGN A68).
	root = engineSwitcher(root, dry)
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
			if status.Enabled && !status.Pending && !dry {
				releaseIdleGroup(root, base, status.ID, emit)
			}
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
		var extra []autoswitch.Option
		if id == fresh {
			extra = append(extra, autoswitch.WithFreshActive())
		}
		engine := newAutoEngine(sw, groupSettings(sw, base), onEvent, dry, sw.OAuth, extra...)
		engines = append(engines, running{sw, engine})
	}
	for _, run := range engines {
		run.engine.Tick()
	}
}

// releaseIdleGroup returns a group's account to the shared pool once its
// sessions have been gone for the group's cooldown (DESIGN A74).
func releaseIdleGroup(root *core.Switcher, base settings.AutoSwitchSettings, id groups.ID, emit func(autoswitch.Event)) {
	sw, err := root.ForGroup(id)
	if err != nil {
		return
	}
	grace := time.Duration(groupSettings(sw, base).CooldownSeconds * float64(time.Second))
	released, err := switching.ReleaseIdleGroup(sw.Store, grace)
	switch {
	case err != nil:
		root.Log.Warningf("%s kept its account: %v", id.Label(), err)
	case released != nil && emit != nil:
		emit(groupEvent{releaseEvent{ts: root.Clk.Now().UTC().Format("2006-01-02T15:04:05Z"), account: *released}, id})
	}
}

type releaseEvent struct {
	ts      string
	account groups.Account
}

func (releaseEvent) Kind() string { return "group-release" }

func (e releaseEvent) JSON() map[string]any {
	number, _ := strconv.Atoi(e.account.Number)
	return map[string]any{"schemaVersion": jsonout.SchemaVersion, "event": e.Kind(), "ts": e.ts,
		"released": map[string]any{"number": number, "email": e.account.Email}}
}

func (e releaseEvent) Human() string {
	return "Released Account-" + e.account.Number + " (" + e.account.Email + ") to the shared pool; its sessions have ended"
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
