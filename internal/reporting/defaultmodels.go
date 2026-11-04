package reporting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

const (
	defaultModelsFile = "default_models.json"
	// The status line reruns every few seconds; an unchanged model is
	// rewritten at most this often, a changed one at once.
	defaultModelRefresh = time.Minute
	// Records of sessions that no longer run are dropped after a day.
	defaultModelKeep = 24 * time.Hour
)

type defaultSessionModel struct {
	Family string  `json:"family"`
	SeenAt float64 `json:"seenAt"`
}

func readDefaultSessionModels(root string) map[string]defaultSessionModel {
	var file struct {
		Sessions map[string]defaultSessionModel `json:"sessions"`
	}
	if raw, err := os.ReadFile(filepath.Join(root, defaultModelsFile)); err == nil {
		_ = json.Unmarshal(raw, &file)
	}
	if file.Sessions == nil {
		file.Sessions = map[string]defaultSessionModel{}
	}
	return file.Sessions
}

// RecordDefaultSessionModel remembers the model family a default-login session
// runs, as its status line reports it (DESIGN A76). An unknown model or a busy
// lock records nothing.
func RecordDefaultSessionModel(root, sessionID, model string, now time.Time) error {
	family, err := routingstate.Family(model)
	if sessionID == "" || err != nil {
		return nil
	}
	lock := filelock.New(filepath.Join(root, ".default_models.lock"), 50*time.Millisecond)
	if ok, err := lock.Acquire(0); err != nil || !ok {
		return err
	}
	defer lock.Release()
	sessions := readDefaultSessionModels(root)
	at := float64(now.UnixNano()) / 1e9
	if old, ok := sessions[sessionID]; ok && old.Family == family && at-old.SeenAt < defaultModelRefresh.Seconds() && at >= old.SeenAt {
		return nil
	}
	sessions[sessionID] = defaultSessionModel{Family: family, SeenAt: at}
	for id, rec := range sessions {
		if at-rec.SeenAt > defaultModelKeep.Seconds() {
			delete(sessions, id)
		}
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "sessions": sessions})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(root, defaultModelsFile), raw, atomicfile.Opts{})
}

// DefaultSessionSettings counts, for the default login, the model limits of
// the families its live sessions run, when autoswitch.model is unset (DESIGN
// A76). A set autoswitch.model always wins; no record counts nothing.
func DefaultSessionSettings(s *store.Store, base settings.AutoSwitchSettings) settings.AutoSwitchSettings {
	if base.Model != nil {
		return base
	}
	records := readDefaultSessionModels(s.BackupDir())
	if len(records) == 0 {
		return base
	}
	sessions, _, err := procdetect.GetRunningInstancesErr(s.DefaultProfileDir())
	if err != nil {
		return base
	}
	families := map[string]bool{}
	for _, session := range sessions {
		if rec, ok := records[session.SessionID]; ok {
			families[rec.Family] = true
		}
	}
	if model := strings.Join(scopedNamesFor(s, families), ","); model != "" {
		base.Model = &model
	}
	return base
}
