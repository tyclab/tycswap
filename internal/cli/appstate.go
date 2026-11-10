package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

const appStateVersion = 1

// appState is what the dashboard keeps in ui_prefs.json: the folded cards
// (A27). The app's old auto-switch choice there moves to settings.json once
// (migrateAutoSwitchChoice).
type appState struct {
	Folded map[string]bool
}

// prefsLockFilename orders every read-modify-write of ui_prefs.json across
// processes, as .settings.lock does for settings.json: the app saves folds
// while any command may be running its one-time migration.
const prefsLockFilename = ".ui_prefs.lock"

// appStateMu orders this process's writers without the file lock's poll;
// only the file lock orders processes.
var appStateMu sync.Mutex

// readPrefs decodes ui_prefs.json as a raw object, so a rewrite keeps every
// key it does not know. A missing or corrupt file is nil, without an error.
func readPrefs(path string) (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil, nil
	}
	return doc, nil
}

// editPrefs is every read-modify-write of ui_prefs.json: under the lock it
// reads the file ({} when missing or corrupt), lets change edit the object,
// and writes it back when change reports an edit. A file it cannot read, or
// a change that fails, leaves the file as it was.
func editPrefs(path string, change func(doc map[string]json.RawMessage) (bool, error)) error {
	appStateMu.Lock()
	defer appStateMu.Unlock()
	return filelock.New(filepath.Join(filepath.Dir(path), prefsLockFilename), 0).With(func() error {
		doc, err := readPrefs(path)
		if err != nil {
			return err
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		if edited, err := change(doc); err != nil || !edited {
			return err
		}
		return atomicfile.WriteJSON(path, doc, atomicfile.Opts{FileMode: 0o600, DirMode: 0o700})
	})
}

// stateOf reads the folded cards out of doc; a malformed "folded" is none.
func stateOf(doc map[string]json.RawMessage) appState {
	var st appState
	if json.Unmarshal(doc["folded"], &st.Folded) != nil {
		st.Folded = nil
	}
	return st
}

func updateAppState(path string, change func(*appState)) error {
	return editPrefs(path, func(doc map[string]json.RawMessage) (bool, error) {
		st := stateOf(doc)
		change(&st)
		doc["version"] = json.RawMessage(strconv.Itoa(appStateVersion))
		if len(st.Folded) == 0 {
			delete(doc, "folded")
		} else {
			doc["folded"], _ = json.Marshal(st.Folded)
		}
		return true, nil
	})
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
	doc, _ := readPrefs(path)
	return stateOf(doc)
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
	// Unlocked, only to keep the lock off every command once nothing is left.
	doc, err := readPrefs(path)
	if err != nil {
		return err
	}
	if _, present := doc[legacyAutoSwitchKey]; !present {
		return nil // nothing remembered, a missing or corrupt file included
	}
	beforePrefsLock()
	// Decide again and seed under one hold of the lock: another process may
	// have moved the choice since the read above and the user then unset the
	// key, which a second seed would undo. The order .ui_prefs.lock, then
	// .settings.lock has no reverse path: settings never calls back here.
	return editPrefs(path, func(doc map[string]json.RawMessage) (bool, error) {
		choice, present := doc[legacyAutoSwitchKey]
		if !present {
			return false, nil
		}
		var on bool
		if json.Unmarshal(choice, &on) == nil && on {
			if _, err := seedEnabled(root); err != nil {
				return false, err
			}
		}
		delete(doc, legacyAutoSwitchKey)
		return true, nil
	})
}

// seedEnabled turns autoswitch.enabled on unless settings.json already sets
// it either way; a variable so a test can act while the seed runs.
var seedEnabled = func(root string) (bool, error) {
	return settings.SeedSetting(root, "autoswitch.enabled", "true")
}

// beforePrefsLock runs between the unlocked check and the lock; a test lets
// another writer in there.
var beforePrefsLock = func() {}
