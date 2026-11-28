// A linked start comes up on a start login's own login where it would
// otherwise switch or hold a shared login (DESIGN A125).
package cli

import (
	"io"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

// ownStartLogin is the login of its own a linked start comes up on: the ready
// Mythos login of a start login, one with room by start's ranking first, else
// the first in sequence order. "" when no start login has one; enrolled then
// says whether one sign-in would give a start login one. It reads files only;
// s is the default store.
func ownStartLogin(s *store.Store) (dir, num string, enrolled bool) {
	view := s.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	data, err := view.SequenceForUpdate()
	if err != nil {
		return "", "", false
	}
	owns, enrolled := view.OwnStartLogins(data)
	if len(owns) == 0 {
		return "", "", enrolled
	}
	set, err := loadStartSettings(view.BackupDir())
	if err != nil {
		return "", "", false
	}
	snap := autoswitch.StartSnapshot{Settings: set, Entries: (&core.Switcher{Store: view}).UsageEntriesByAccount(map[string]bool{})}
	now := view.Clk.Now()
	for _, own := range owns {
		if autoswitch.StartLoginHasRoom(snap, own.Num, now) {
			return own.Dir, own.Num, true
		}
	}
	return owns[0].Dir, owns[0].Num, true
}

func ownLoginNotice(num string) string {
	return "; starting on start login Account-" + num + "'s Mythos login."
}

// ownLoginMissing is the rest of start's notice where it would have come up
// on an own login and a start login lost its own: the selection switches, or
// holds, login for every session on it (DESIGN A126).
func ownLoginMissing(login string, holds bool) string {
	verb := "switches"
	if holds {
		verb = "holds"
	}
	return "; no start login has a Mythos login of its own, so this start " + verb + " " + login + " for every session on it. " + reporting.OwnStartLoginHint
}

// ownLoginInstead is the own login a linked start of family comes up on
// instead of the default login, with its notice: when start's selection there
// would switch or hold the login. "" leaves the start to that selection,
// after a notice that says so when a start login lost its own (DESIGN A125,
// A126).
func ownLoginInstead(family string, stderr io.Writer) string {
	if startStoreProblem(paths.GetBackupRoot()) != "" {
		return ""
	}
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return ""
	}
	if family == mythosFamily && !anyMythosMark(sw) {
		family = ""
	}
	snap, err := readStartSnapshotFor(sw, "", family)
	if err != nil || autoswitch.StartKeepsLogin(snap, sw.Clk.Now()) {
		return ""
	}
	why := "Account-" + snap.Current + " is no start login"
	switch {
	case snap.Current == "":
		why = "the default login is on no start login"
	case snap.CurrentDisabled:
		why = "Account-" + snap.Current + " is disabled"
	case snap.CurrentMarked && family == mythosFamily:
		why = "Account-" + snap.Current + " is at its limit"
	case snap.CurrentMarked:
		why = "start login Account-" + snap.Current + " is at its limit"
	case family == mythosFamily:
		why = "Account-" + snap.Current + " is not marked for Mythos"
	}
	dir, num, enrolled := ownStartLogin(sw.Store)
	if dir == "" {
		if enrolled {
			reportStartSelection(sw, stderr, why+ownLoginMissing("the default login", snap.CurrentMarked))
		}
		return ""
	}
	reportStartSelection(sw, stderr, why+ownLoginNotice(num))
	return dir
}
