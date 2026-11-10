package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

const appStateVersion = 1

// appState is ui_prefs.json: the dashboard's folded cards (A27). The app's
// old auto-switch choice in it moves to settings.json once
// (migrateAutoSwitchChoice).
type appState struct {
	Version int             `json:"version"`
	Folded  map[string]bool `json:"folded,omitempty"`
}

// Orders the read-modify-write: concurrent fold writes and the one-time
// migration must not drop each other.
var appStateMu sync.Mutex

func updateAppState(path string, change func(*appState)) error {
	appStateMu.Lock()
	defer appStateMu.Unlock()
	st := loadAppState(path)
	change(&st)
	return saveAppState(path, st)
}

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

func appStatePath(backupDir string) string { return filepath.Join(backupDir, "ui_prefs.json") }

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

// legacyAutoSwitchKey is where the app remembered auto-switch was on (A43),
// readable only by the app; autoswitch.enabled replaces it.
const legacyAutoSwitchKey = "autoSwitch"

// migrateAutoSwitchChoice seeds autoswitch.enabled from the app's remembered
// choice when settings.json does not decide it yet, then drops the old key
// from ui_prefs.json. A failed seed keeps the old key, so the next run tries
// again instead of losing the choice. Any other key in ui_prefs.json stays.
func migrateAutoSwitchChoice(root string) error {
	path := appStatePath(root)
	appStateMu.Lock()
	defer appStateMu.Unlock()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil // the app reads a corrupt file as nothing remembered
	}
	choice, present := doc[legacyAutoSwitchKey]
	if !present {
		return nil
	}
	var on bool
	if json.Unmarshal(choice, &on) == nil && on && !enabledIsSet(root) {
		if _, err := settings.SetSetting(root, "autoswitch.enabled", "true"); err != nil {
			return err
		}
	}
	delete(doc, legacyAutoSwitchKey)
	return atomicfile.WriteJSON(path, doc, atomicfile.Opts{FileMode: 0o600, DirMode: 0o700})
}

// enabledIsSet reports whether settings.json carries autoswitch.enabled, true
// or false: an explicit choice there always wins over the remembered one.
func enabledIsSet(root string) bool {
	for _, e := range settings.EffectiveSettings(root) {
		if e.Spec.Dotted() == "autoswitch.enabled" {
			return e.IsSet
		}
	}
	return false
}
