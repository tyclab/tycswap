// appstate.go — what the app and the dashboard remember for the machine
// (DESIGN A27, A43): the cards the user folded, and whether the user left
// auto-switch on in the tray app.
//
// The dashboard's port changes at every start and browser storage is kept
// per port, so the server keeps the folded cards in the backup root. Auto-
// switch runs inside the app process, so quitting the app — or the restart
// after an update — used to leave it off every time, whatever the user had
// chosen; the app now records the choice when the user turns it on or off
// (tray or dashboard) and resumes it at start. Only the app does that: a
// separate `tycswap web` dashboard never starts an engine by itself, or two
// processes would rotate the same accounts.
//
// Both live in one file, <backup_root>/ui_prefs.json:
// {"version": 1, "autoSwitch": true, "folded": {"<card>": true}}.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/web"
)

// appStateVersion is the file's schema version.
const appStateVersion = 1

// appState is <backup_root>/ui_prefs.json.
type appState struct {
	Version int `json:"version"`
	// AutoSwitch: the user left auto-switch on in the app (a dry run is not
	// recorded, and `tycswap web` records nothing).
	AutoSwitch bool `json:"autoSwitch,omitempty"`
	// Folded are the dashboard's cards the user folded (a card → true); the
	// app's and a separate `tycswap web` dashboard share them.
	Folded map[string]bool `json:"folded,omitempty"`
}

// appStateMu orders the read-modify-write of the file in this process: the
// auto-switch choice and the folded cards are written from different
// requests, two pages may fold two cards at once, and none may drop the
// other's change.
var appStateMu sync.Mutex

// updateAppState applies change to the file under appStateMu.
func updateAppState(path string, change func(*appState)) error {
	appStateMu.Lock()
	defer appStateMu.Unlock()
	st := loadAppState(path)
	change(&st)
	return saveAppState(path, st)
}

// uiPrefs is the dashboard's web.UIPrefs: the folded cards, in the app state
// file.
type uiPrefs struct{ path string }

var _ web.UIPrefs = uiPrefs{}

func (u uiPrefs) Folded() map[string]bool {
	appStateMu.Lock()
	defer appStateMu.Unlock()
	return loadAppState(u.path).Folded
}

func (u uiPrefs) SetFolded(card string, folded bool) error {
	return updateAppState(u.path, func(st *appState) {
		if !folded {
			delete(st.Folded, card)
			return
		}
		if st.Folded == nil {
			st.Folded = map[string]bool{}
		}
		st.Folded[card] = true
	})
}

// appStatePath is <backup_root>/ui_prefs.json, the file A27 introduced for
// the folded cards.
func appStatePath(backupDir string) string { return filepath.Join(backupDir, "ui_prefs.json") }

// loadAppState reads the file; a missing or unreadable one is the zero
// state (nothing folded, auto-switch off), which is what the app did before
// it remembered anything.
func loadAppState(path string) appState {
	raw, err := os.ReadFile(path)
	if err != nil {
		return appState{}
	}
	var st appState
	if json.Unmarshal(raw, &st) != nil {
		return appState{}
	}
	return st
}

func saveAppState(path string, st appState) error {
	st.Version = appStateVersion
	return atomicfile.WriteJSON(path, st, atomicfile.Opts{FileMode: 0o600, DirMode: 0o700})
}
