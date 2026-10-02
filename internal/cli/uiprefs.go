// uiprefs.go — what the dashboard remembers for the machine (DESIGN A27):
// the cards the user folded. The dashboard's port changes at every start and
// browser storage is kept per port, so the server keeps the choice in the
// backup root, `ui_prefs.json`, where every `tycswap web` finds it.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/web"
)

// uiPrefsVersion is the file's schema version.
const uiPrefsVersion = 1

// uiPrefsFile is <backup_root>/ui_prefs.json.
type uiPrefsFile struct {
	Version int `json:"version"`
	// Folded are the dashboard's cards the user folded (a card → true).
	Folded map[string]bool `json:"folded,omitempty"`
}

// uiPrefsMu orders the read-modify-write of the file in this process: two
// pages folding two cards at once must not drop each other's change.
var uiPrefsMu sync.Mutex

// uiPrefs is the dashboard's web.UIPrefs.
type uiPrefs struct{ path string }

var _ web.UIPrefs = uiPrefs{}

func uiPrefsPath(backupDir string) string { return filepath.Join(backupDir, "ui_prefs.json") }

func (u uiPrefs) Folded() map[string]bool {
	uiPrefsMu.Lock()
	defer uiPrefsMu.Unlock()
	return loadUIPrefs(u.path).Folded
}

func (u uiPrefs) SetFolded(card string, folded bool) error {
	uiPrefsMu.Lock()
	defer uiPrefsMu.Unlock()
	st := loadUIPrefs(u.path)
	if !folded {
		delete(st.Folded, card)
	} else {
		if st.Folded == nil {
			st.Folded = map[string]bool{}
		}
		st.Folded[card] = true
	}
	st.Version = uiPrefsVersion
	return atomicfile.WriteJSON(u.path, st, atomicfile.Opts{})
}

// loadUIPrefs reads the file; a missing or unreadable one is the zero
// state (nothing folded).
func loadUIPrefs(path string) uiPrefsFile {
	raw, err := os.ReadFile(path)
	if err != nil {
		return uiPrefsFile{}
	}
	var st uiPrefsFile
	if json.Unmarshal(raw, &st) != nil {
		return uiPrefsFile{}
	}
	return st
}
