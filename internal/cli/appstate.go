package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/web"
)

const appStateVersion = 1

type appState struct {
	Version    int             `json:"version"`
	AutoSwitch bool            `json:"autoSwitch,omitempty"`
	Folded     map[string]bool `json:"folded,omitempty"`
}

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
