package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
)

func mythosStatePath(root string) string {
	return autoswitch.StatePath(filepath.Join(root, "mythos"))
}

// switchMythos commits the box's choice even when the client is refreshing a
// particular session. Each pending move rechecks that choice under the root lock.
func switchMythos(sw *core.Switcher, id string) (map[string]any, error) {
	root := sw.BackupDir()
	var num, email, dir string
	var changed bool
	err := sw.WithRosterLocked(func(data *store.SequenceData) error {
		var err error
		num, email, _, err = sw.ResolveAccountFrom(data, id)
		if err != nil {
			return err
		}
		login, err := sw.MythosLogin(data, num)
		if err != nil {
			return err
		}
		if !login.Ready {
			return cerr.Validation("Account-%s has no enabled, marked, ready Mythos login", num)
		}
		dir = login.Dir
		selected, err := store.SelectedMythosDigest(root)
		if err != nil {
			return err
		}
		var stampErr error
		if err := autoswitch.HandSwitch(mythosStatePath(root), sw.Clk, func(err error) { stampErr = err }, func() (string, error) {
			if selected != filepath.Base(dir) {
				if err := store.SelectMythosLogin(root, dir); err != nil {
					return "", err
				}
				changed = true
			}
			return num, nil
		}); err != nil {
			return err
		}
		if stampErr != nil {
			return fmt.Errorf("selected Account-%s but could not hold automatic Mythos moves: %w", num, stampErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Resolve the full account identity, including its organization, again on each move.
	links, err := reporting.LinkedSessions(root)
	if err != nil {
		return nil, fmt.Errorf("selected Account-%s, but could not read its sessions: %w", num, err)
	}
	moved := 0
	var pending []error
	for _, link := range links {
		if !wantsMythosLogin(link) {
			continue
		}
		redirected := false
		err := reporting.MoveSessionLink(sw.Store, "switch --store mythos", link.ID, filepath.Join(root, "links", link.Token), false, func(current *reporting.SessionLink) error {
			if !wantsMythosLogin(*current) {
				return nil
			}
			data, err := sw.SequenceForUpdate()
			if err != nil {
				return err
			}
			login, err := sw.MythosLogin(data, num)
			if err != nil {
				return err
			}
			target := login.Dir
			if !login.Ready {
				return cerr.Validation("Account-%s is no longer ready for Mythos", num)
			}
			selected, err := store.SelectedMythosDigest(root)
			if err != nil {
				return err
			}
			if filepath.Base(target) != selected || target != dir {
				return cerr.Switch("the selected Mythos login changed")
			}
			redirected = current.Target != target
			current.Target = target
			return nil
		})
		if err != nil {
			pending = append(pending, fmt.Errorf("session %s: %w", link.ID, err))
		} else if redirected {
			moved++
		}
	}
	if err := errors.Join(pending...); err != nil {
		return nil, fmt.Errorf("selected Account-%s for Mythos. Session moves pending: %w", num, err)
	}
	reason := "already-active"
	if changed {
		reason = "switched"
	}
	message := fmt.Sprintf("Mythos login: Account-%s (%s).", num, termsafe.Strip(email))
	switch moved {
	case 0:
	case 1:
		message += " Moved one linked session."
	default:
		message += fmt.Sprintf(" Moved %d linked sessions.", moved)
	}
	return map[string]any{"message": message, "schemaVersion": jsonout.SchemaVersion, "store": "mythos", "reason": reason, "switched": changed, "to": map[string]any{"number": slotNumber(num), "email": email}, "movedSessions": moved}, nil
}
