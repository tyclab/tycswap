package migrations

import (
	"encoding/json"
	"os"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/clock"
)

const stateVersion = 1

type stateFile struct {
	Version int                        `json:"version"`
	Applied map[string]json.RawMessage `json:"applied"`
}

// loadApplied returns the {migration_id: raw-value} map, or {} if the file is
// missing, unparseable, not a JSON object, or has no usable "applied" object.
// Never returns an error — see the package doc.
func loadApplied(path string) map[string]json.RawMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]json.RawMessage{}
	}
	var parsed struct {
		Applied map[string]json.RawMessage `json:"applied"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return map[string]json.RawMessage{}
	}
	if parsed.Applied == nil {
		return map[string]json.RawMessage{}
	}
	return parsed.Applied
}

func markApplied(path string, clk clock.Clock, migrationID string) error {
	applied := loadApplied(path)
	ts, err := json.Marshal(clk.Now().UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		return err
	}
	if applied == nil {
		applied = map[string]json.RawMessage{}
	}
	applied[migrationID] = ts
	return atomicfile.WriteJSON(path, stateFile{Version: stateVersion, Applied: applied}, atomicfile.Opts{})
}

// dirExists reports whether path exists, treating any stat error (including a
// permission failure on an unsearchable parent) as "missing" — spec 03§9.3's
// Path.exists() normalization, mirrored throughout the port (e.g.
// credstore.exists).
func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
