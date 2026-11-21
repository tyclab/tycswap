package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

// The caller verified the current placement under the root store lock.
func rememberMythosPlacement(view *store.Store, proposed mythosPlacement) error {
	root := view.BackupDir()
	selected, err := store.SelectedMythosDigest(root)
	if err != nil {
		return err
	}
	if selected == filepath.Base(proposed.dir) {
		return nil
	}
	return store.SelectMythosLogin(root, proposed.dir)
}

func wantsMythosLogin(rec reporting.SessionLink) bool {
	return rec.Wanted == mythosFamily || rec.Wanted == "" && rec.Family == mythosFamily
}

func runsOnMythos(root string, rec reporting.SessionLink) bool {
	return isMythosDirectory(root, rec.Target) && wantsMythosLogin(rec)
}

type mythosCooldown string

func (h mythosCooldown) Error() string { return string(h) }

var errMythosAllocation = errors.New("Mythos session move requires an allocation decision")

func tickMythosSelection(sw *core.Switcher, set settings.AutoSwitchSettings, dryRun bool) (next mythosPlacement, err error) {
	root := sw.BackupDir()
	if selected, err := store.SelectedMythosDigest(root); selected == "" || err != nil {
		return next, err
	}
	choose := func() error {
		if !dryRun {
			// A missing roster cannot establish that the selected identity was removed.
			data, err := sw.ReadSequence()
			if err != nil {
				return err
			}
			if err := sw.ClearInvalidMythosSelection(data); err != nil {
				return err
			}
		}
		selected, err := store.SelectedMythosDigest(root)
		if err != nil || selected == "" || !dryRun && !autoSwitchOn(root) {
			return err
		}
		_, hold, err := handMythosChoice(sw.Store, selected, set, dryRun)
		if err != nil {
			return err
		}
		if hold != "" {
			return mythosCooldown(hold)
		}
		next, err = chooseMythosLogin(sw.Store, mythosFamily, "", "", filepath.Join(store.MythosLoginRoot(root), selected), selected, set)
		if err != nil || next.dir == "" {
			return err
		}
		if next.allocation != "" {
			return fmt.Errorf("%w for Account-%s", errMythosAllocation, next.number)
		}
		if dryRun {
			return nil
		}
		return rememberMythosPlacement(sw.Store, next)
	}
	if dryRun {
		err = choose()
	} else {
		err = sw.Lock.With(choose)
	}
	return next, err
}

type mythosSwitchEvent struct {
	autoswitch.SwitchEvent
	session string
}

func (e mythosSwitchEvent) JSON() map[string]any {
	fields := e.SwitchEvent.JSON()
	fields["store"] = "mythos"
	if e.session != "" {
		fields["sessionId"] = e.session
	}
	return fields
}

func (e mythosSwitchEvent) Human() string {
	subject := "Mythos login"
	if e.session != "" {
		subject = "Mythos session " + e.session
	}
	verb := "switched"
	if e.DryRun {
		verb = "would switch"
	}
	return fmt.Sprintf("%s %s to Account-%v", subject, verb, e.ToRef["number"])
}

func mythosTick(sw *core.Switcher, set settings.AutoSwitchSettings, emit func(autoswitch.Event), dryRun bool) autoswitch.TickOutcome {
	root := sw.BackupDir()
	if runtime.GOOS != "linux" {
		return autoswitch.NoAction
	}

	ts := sw.Clk.Now().UTC().Format("2006-01-02T15:04:05Z")
	failure := func(err error) autoswitch.TickOutcome {
		emit(autoswitch.ErrorEvent{Ts: ts, Message: err.Error()})
		return autoswitch.Error
	}
	if !dryRun {
		if err := reporting.PruneSessionLinks(root); err != nil {
			return failure(err)
		}
	}
	sayMove := func(p mythosPlacement, session string) {
		emit(mythosSwitchEvent{autoswitch.SwitchEvent{Ts: ts, Trigger: "mythos-at-limit", ToRef: map[string]any{"number": slotNumber(p.number)}, DryRun: dryRun}, session})
	}
	problem := func(err error) autoswitch.TickOutcome {
		var hold mythosCooldown
		if errors.As(err, &hold) {
			emit(autoswitch.NoSwitchEvent{Ts: ts, Reason: "cooldown", Detail: hold.Error()})
			return autoswitch.NoAction
		}
		if errors.Is(err, errNoMythosQuota) || errors.Is(err, errNoMythosLogin) || errors.Is(err, errMythosAllocation) || cerr.TypeName(err) == string(cerr.KindClaudeCodeLockTimeout) {
			emit(autoswitch.NoSwitchEvent{Ts: ts, Reason: "mythos-session", Detail: err.Error()})
			return autoswitch.Blocked
		}
		return failure(err)
	}
	outcome := autoswitch.NoAction
	selected, err := tickMythosSelection(sw, set, dryRun)
	if err != nil {
		outcome = problem(err)
	} else if selected.dir != "" {
		sayMove(selected, "")
		outcome = autoswitch.Switched
	}
	if _, err := os.Lstat(filepath.Join(root, "links")); os.IsNotExist(err) {
		return outcome
	}
	links, err := reporting.LinkedSessions(root)
	if err != nil {
		return failure(err)
	}
	for _, link := range links {
		if !runsOnMythos(root, link) {
			continue
		}
		var placement mythosPlacement
		apply := func(current *reporting.SessionLink) error {
			if !runsOnMythos(root, *current) {
				return nil
			}
			var err error
			currentSettings := set
			if !dryRun {
				currentSettings.Enabled = autoSwitchOn(root)
			}
			placement, err = mythosTickPlacement(sw.Store, current.Target, currentSettings, dryRun)
			if err != nil {
				return err
			}
			if placement.dir == "" {
				return nil
			}
			if placement.allocation != "" {
				return fmt.Errorf("%w for Account-%s", errMythosAllocation, placement.number)
			}
			if !dryRun {
				if err := rememberMythosPlacement(sw.Store, placement); err != nil {
					return err
				}
			}
			current.Target = placement.dir
			return nil
		}
		if dryRun {
			err = apply(&link)
		} else {
			err = reporting.MoveSessionLink(sw.Store, "tick", link.ID, filepath.Join(root, "links", link.Token), false, apply)
		}
		if err != nil {
			outcome = significantOutcome(outcome, problem(err))
		} else if placement.dir != "" {
			sayMove(placement, link.ID)
			outcome = significantOutcome(outcome, autoswitch.Switched)
		}
	}
	return outcome
}
