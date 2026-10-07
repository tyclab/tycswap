package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

func groupsCommand(prog string, args []string, streams ioStreams) int {
	if len(args) > 0 && args[0] == "guard" {
		if len(args) != 3 || args[1] != "--group" {
			return subError(prog+" groups guard", streams.err, "expected --group fable|opus")
		}
		id, err := groups.Parse(args[2])
		var event struct {
			ToModel   string `json:"to_model"`
			FromModel string `json:"from_model"`
			SessionID string `json:"session_id"`
		}
		if err == nil {
			err = json.NewDecoder(io.LimitReader(streams.in, 1<<20)).Decode(&event)
		}
		if err == nil {
			err = groups.CheckModel(id, event.ToModel)
		}
		if err == nil && event.SessionID != "" {
			err = recovery.NewStore(paths.GetBackupRoot()).ModelChange(event.SessionID, event.FromModel, event.ToModel, time.Now())
		}
		if err != nil {
			fmt.Fprintln(streams.err, err)
			return 2
		}
		return 0
	}
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintf(streams.out, "usage: %s groups [--json]\n       %s groups switch fable|opus ACCOUNT\n       %s groups config fable|opus KEY VALUE\n       %s groups reconcile fable|opus\n       %s groups capability ACCOUNT start|continue true|false\n", prog, prog, prog, prog, prog)
		return 0
	}
	sw, err := constructSwitcher(false, streams.err)
	if err != nil {
		return renderDomainError(err, true, streams.out, streams.err)
	}
	if code, blocked := guardRoot(streams.err); blocked {
		return code
	}
	switch {
	case len(args) == 2 && args[0] == "reconcile":
		if err := (groupFacade{sw}).Reconcile(args[1]); err != nil {
			return renderDomainError(err, true, streams.out, streams.err)
		}
		return 0
	case len(args) == 4 && args[0] == "config":
		id, err := groups.Parse(args[1])
		if err != nil {
			return subError(prog+" groups", streams.err, err.Error())
		}
		if args[2] == "autoswitch.model" {
			return subError(prog+" groups", streams.err, "group model limits follow its live sessions")
		}
		result, err := (settingsFacade{root: groups.ScopeDir(sw.BackupDir(), id)}).Set(args[2], args[3])
		if err != nil {
			return renderDomainError(err, true, streams.out, streams.err)
		}
		writeJSONIndent(streams.out, result)
		return 0
	case len(args) == 0 || (len(args) == 1 && args[0] == "--json"):
		status, err := sw.GroupStatuses()
		if err != nil {
			return renderDomainError(err, true, streams.out, streams.err)
		}
		writeJSONIndent(streams.out, status)
		return 0
	case len(args) == 3 && args[0] == "switch":
		result, err := (groupFacade{sw}).Switch(args[1], args[2])
		if err != nil {
			return renderDomainError(err, true, streams.out, streams.err)
		}
		writeJSONIndent(streams.out, result)
		return 0
	case len(args) == 4 && args[0] == "capability":
		allowed, err := strconv.ParseBool(args[3])
		if err != nil {
			return subError(prog+" groups", streams.err, "capability expects true or false")
		}
		intent := groups.Intent(args[2])
		if intent != groups.Start && intent != groups.Continue {
			return subError(prog+" groups", streams.err, "capability expects start or continue")
		}
		if err := sw.SetGroupCapability(args[1], intent, allowed); err != nil {
			return renderDomainError(err, true, streams.out, streams.err)
		}
		return 0
	default:
		return subError(prog+" groups", streams.err, "expected --json, switch GROUP ACCOUNT, or capability ACCOUNT start|continue true|false")
	}
}

type groupFacade struct{ sw *core.Switcher }

func (f groupFacade) Views() []web.GroupView {
	statuses, err := f.sw.GroupStatuses()
	if err != nil {
		return []web.GroupView{{Blocker: err.Error()}}
	}
	views := make([]web.GroupView, 0, len(statuses))
	roster, rosterErr := f.sw.ReadSequence()
	observed, observeErr := recovery.NewStore(f.sw.BackupDir()).Snapshot()
	for _, status := range statuses {
		v := web.GroupView{ID: string(status.ID), Label: status.Label, LiveSessions: status.LiveSessions, Blocker: status.Blocker, Pending: status.Pending, AccountBlockers: map[string]string{}}
		if status.ActiveNumber != nil {
			v.ActiveNumber = *status.ActiveNumber
		}
		scope, err := f.sw.ForGroup(status.ID)
		if err != nil {
			v.Blocker = err.Error()
			views = append(views, v)
			continue
		}
		v.Settings = settings.ValuesOf(groupSettings(scope, settings.Load(f.sw.BackupDir())))
		if status.LiveSessions > 0 && v.Blocker == "" {
			if observeErr != nil {
				v.Blocker = "session model record is unreadable"
			} else {
				v.Blocker = groupModelBlocker(scope, observed)
			}
		}
		if rosterErr != nil || roster == nil {
			v.Blocker = "account roster is unreadable"
			views = append(views, v)
			continue
		}
		for _, number := range roster.Sequence {
			num := strconv.Itoa(number)
			owner, err := f.sw.CredentialOwner(num)
			compat := scope.GroupCompatibility(num, groups.Continue)
			switch {
			case err != nil:
				v.AccountBlockers[num] = "ownership unknown"
			case owner.Uncertain:
				v.AccountBlockers[num] = "ownership needs reconciliation"
			case owner.Scope != "" && owner.Scope != v.ID:
				v.AccountBlockers[num] = "in use by " + owner.Scope
			case !compat.Known || !compat.Allowed:
				v.AccountBlockers[num] = compat.Reason
			}
		}
		views = append(views, v)
	}
	return views
}

func (f groupFacade) Switch(group, account string) (map[string]any, error) {
	id, err := groups.Parse(group)
	if err != nil {
		return nil, cerr.Validation("%s", err)
	}
	sw, err := f.sw.ForGroup(id)
	if err != nil {
		return nil, err
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, cerr.Validation("choose an account")
	}
	return sw.SwitchTo(account, true)
}
