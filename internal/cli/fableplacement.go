// The Fable placement rule (DESIGN A112): start, the status line and the tick
// give an automatically placed Fable session one answer.
package cli

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

// noFableRoom is why the rule sends a Fable session to the default login.
const noFableRoom = "no Fable login has room"

// fableRoomKey is the Fable state file's key holding the accounts the last
// warning about Fable room without a Fable login named.
const fableRoomKey = "lastFableRoomWithoutLogin"

// fableSessionOnDefault is the rule for an automatically placed Fable session
// the Fable store would serve (on, usable, not sent away by A105.6): it
// belongs on the default login when no account the Fable store can hold has
// room for Fable and the default login's current account, verified by its
// credential and enabled, has; otherwise on the Fable store, as before. notice
// says why and where; hinted: it ends in A112's hint. It reads files only; s
// is the default store.
func fableSessionOnDefault(s *store.Store) (toDefault bool, notice string, hinted bool) {
	return fableRoomRule(placementFor(s, false))
}

// fableRoomRule is fableSessionOnDefault on p's inputs.
func fableRoomRule(p *placementInputs) (toDefault bool, notice string, hinted bool) {
	view := p.view
	room := reporting.ReadFableRoom(view)
	if room.Room {
		return false, "", false
	}
	login := p.login("", view)
	if login.err != nil {
		return false, "", false
	}
	if _, _, _, err := login.owner.of("default"); err != nil {
		return false, "", false
	}
	now := view.Clk.Now()
	if limits, _ := modelLimitWindows(login.entry, "fable", now); len(limits) > 0 {
		return false, "", false
	}
	notice = "Fable store: " + noFableRoom
	if room.Back != "" {
		notice += " (first back: Account-" + room.Back + " at " + oauth.ResetClockString(room.BackAt, now) + ")"
	}
	notice += "; starting on the default login."
	hint := reporting.FableRoomHint(room.WithoutLogin)
	if hint != "" {
		notice += " " + hint
	}
	return true, notice, hint != ""
}

// noteFableSignIn names, on stderr only, the accounts that need a Fable
// sign-in, for a start that reached the Fable start selection (DESIGN A115).
// It reads files only; s is the default store.
func noteFableSignIn(s *store.Store, stderr io.Writer) {
	view := s.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	if hint := reporting.FableSignInHint(reporting.FableSignInNeeded(view)); hint != "" {
		startNotice(stderr, hint)
	}
}

// placeFableSessions follows a real Fable tick. It warns once per set of
// accounts with Fable room but no Fable login while no Fable login has room,
// and gives each live linked session recorded as Fable without a held choice
// the status line's automatic placement, in either direction, through the
// move guard with one try of the refresh lock. A move that needs an allocation
// decision is left alone: the tick never asks.
func placeFableSessions(sw *core.Switcher) {
	root := sw.BackupDir()
	noteFableRoom(sw)
	if runtime.GOOS != "linux" {
		return
	}
	if _, err := os.Lstat(filepath.Join(root, "links")); err != nil {
		return
	}
	links, err := reporting.LinkedSessions(root)
	if err != nil {
		sw.Log.Debugf("Fable session placement: %v", err)
		return
	}
	for _, link := range links {
		if link.Family != "fable" || link.Wanted != "" {
			continue
		}
		err := reporting.MoveSessionLink(sw.Store, "tick", link.ID, filepath.Join(root, "links", link.Token), false, func(locked *store.Store, rec *reporting.SessionLink) error {
			if rec.Family != "fable" || rec.Wanted != "" || !autoSwitchOn(root) {
				return nil
			}
			target, _, err := sessionDestination(placementFor(locked, false), rec, "fable", "fable", false)
			if err != nil || target.dir == "" || target.dir == rec.Target || target.allocation != "" {
				return err
			}
			rec.Target, rec.Question = target.dir, ""
			return nil
		})
		if err != nil {
			sw.Log.Debugf("Fable session placement of %s: %v", link.ID[:min(8, len(link.ID))], err)
		}
	}
}

// noteFableRoom keeps in the Fable state file the accounts with Fable room but
// no Fable login while no account the Fable store can hold has room, and logs
// one warning when that set changes, as a blocked tick reports the accounts
// it names; the set is forgotten once there is room.
func noteFableRoom(sw *core.Switcher) {
	f, err := sw.Store.ForFable()
	if err != nil {
		return
	}
	view := sw.Store.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	var named []string
	if room := reporting.ReadFableRoom(view); !room.Room {
		named = room.WithoutLogin
	}
	changed, err := autoswitch.RememberSet(f.StatePath(), fableRoomKey, strings.Join(named, ","))
	if err != nil {
		sw.Log.Warningf("Fable store: the accounts with Fable room but no Fable login were not recorded: %v; they are checked again next tick", err)
		return
	}
	if changed && len(named) > 0 {
		sw.Log.Warningf("Fable store: %s; %s", noFableRoom, reporting.FableRoomHint(named))
	}
}
