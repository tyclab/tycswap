// A linked start comes up on a start login's own login where it would
// otherwise switch or hold a shared login (DESIGN A125).
package cli

import (
	"io"
	"strconv"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

// ownStartLogin is the login of its own a linked start comes up on: the ready
// Mythos login of a start login, one with room by start's ranking first, else
// the first in sequence order. "" when no start login has one. It reads files
// only; s is the default store.
func ownStartLogin(s *store.Store) (dir, num string) {
	view := s.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	data, err := view.SequenceForUpdate()
	if err != nil {
		return "", ""
	}
	set, err := loadStartSettings(view.BackupDir())
	if err != nil {
		return "", ""
	}
	snap := autoswitch.StartSnapshot{Settings: set, Entries: (&core.Switcher{Store: view}).UsageEntriesByAccount(map[string]bool{})}
	now := view.Clk.Now()
	for _, n := range data.Sequence {
		id := strconv.Itoa(n)
		login, ready := view.MythosLoginReady(data, id)
		if !ready || !store.StartLoginFrom(data, id) {
			continue
		}
		if autoswitch.StartLoginHasRoom(snap, id, now) {
			return login, id
		}
		if dir == "" {
			dir, num = login, id
		}
	}
	return dir, num
}

func ownLoginNotice(num string) string {
	return "; starting on start login Account-" + num + "'s Mythos login."
}

// ownLoginInstead is the own login a linked start of family comes up on
// instead of the default login, with its notice: when start's selection there
// would switch or hold the login. "" leaves the start to that selection.
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
	dir, num := ownStartLogin(sw.Store)
	if dir == "" {
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
	reportStartSelection(sw, stderr, why+ownLoginNotice(num))
	return dir
}
