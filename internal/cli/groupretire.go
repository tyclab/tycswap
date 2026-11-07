package cli

import (
	"errors"
	"strconv"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/jsonout"
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

// retireGroups retires every session group whose profile no session uses any
// more, on each tick that is not a dry run (DESIGN A78). A group whose login
// cannot be verified keeps its claim and is retried on the next tick.
func retireGroups(root *core.Switcher, emit func(autoswitch.Event), dry bool) {
	if dry {
		return
	}
	for _, id := range groups.All() {
		pending, err := groups.Pending(root.BackupDir(), id)
		if err != nil {
			root.Log.Warningf("%s retirement held: %v", id.Label(), err)
			continue
		}
		if !pending {
			continue
		}
		sw, err := root.ForGroup(id)
		if err != nil {
			continue
		}
		released, err := switching.RetireGroup(sw.Store)
		switch {
		case errors.Is(err, switching.ErrGroupBusy):
			root.Log.Debugf("%s retires once its sessions exit", id.Label())
		case err != nil:
			root.Log.Warningf("%s retirement held: %v", id.Label(), err)
		default:
			root.Log.Infof("Retired session group %s", id.Label())
		}
		if released != nil && emit != nil {
			emit(groupEvent{releaseEvent{ts: root.Clk.Now().UTC().Format("2006-01-02T15:04:05Z"), account: *released}, id})
		}
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
	return "Released Account-" + e.account.Number + " (" + e.account.Email + ") from a retired session group"
}
