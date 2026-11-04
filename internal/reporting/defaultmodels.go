package reporting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/procdetect"
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

// modelFamily is the family of a Claude model id or alias ("fable", "opus",
// "sonnet", "haiku"), "" for anything else.
func modelFamily(model string) string {
	m := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "[1m]")
	for _, ch := range m {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
			return ""
		}
	}
	for _, family := range []string{"fable", "opus", "sonnet", "haiku"} {
		if m == family || strings.HasPrefix(m, "claude-"+family+"-") && len(m) > len("claude-"+family+"-") {
			return family
		}
	}
	return ""
}

// RecordDefaultSessionModel remembers the model family a default-login session
// runs, as its status line reports it (DESIGN A76), and reports whether that
// was news: a session seen for the first time or one that changed family
// (A77). An unknown model or a busy lock records nothing.
func RecordDefaultSessionModel(root, sessionID, model string, now time.Time) (bool, error) {
	family := modelFamily(model)
	if sessionID == "" || family == "" {
		return false, nil
	}
	lock := filelock.New(filepath.Join(root, ".default_models.lock"), 50*time.Millisecond)
	if ok, err := lock.Acquire(0); err != nil || !ok {
		return false, err
	}
	defer lock.Release()
	sessions := readDefaultSessionModels(root)
	at := float64(now.UnixNano()) / 1e9
	old, seen := sessions[sessionID]
	changed := !seen || old.Family != family
	if !changed && at-old.SeenAt < defaultModelRefresh.Seconds() && at >= old.SeenAt {
		return false, nil
	}
	sessions[sessionID] = defaultSessionModel{Family: family, SeenAt: at}
	for id, rec := range sessions {
		if at-rec.SeenAt > defaultModelKeep.Seconds() {
			delete(sessions, id)
		}
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "sessions": sessions})
	if err != nil {
		return false, err
	}
	return changed, atomicfile.Write(filepath.Join(root, defaultModelsFile), raw, atomicfile.Opts{})
}

// DefaultSessionPolicy counts, for the default login, the model limits of the
// families its live sessions run when autoswitch.model is unset (DESIGN A76),
// and names the accounts it leaves to Fable work (A77): while every live
// session has a recorded model, none of them Fable, an account that can run
// Fable ranks last and hands over to one that cannot. A set autoswitch.model
// always wins; a live session of unknown model reserves nothing.
func DefaultSessionPolicy(s *store.Store, base settings.AutoSwitchSettings) (settings.AutoSwitchSettings, func(num string) bool) {
	if base.Model != nil {
		return base, nil
	}
	records := readDefaultSessionModels(s.BackupDir())
	if len(records) == 0 {
		return base, nil
	}
	sessions, _, err := procdetect.GetRunningInstancesErr(s.DefaultProfileDir())
	if err != nil {
		return base, nil
	}
	families := map[string]bool{}
	allKnown := len(sessions) > 0
	for _, session := range sessions {
		rec, ok := records[session.SessionID]
		if !ok {
			allKnown = false
			continue
		}
		families[rec.Family] = true
	}
	if model := strings.Join(scopedNamesFor(s, families), ","); model != "" {
		base.Model = &model
	}
	if !allKnown || families["fable"] {
		return base, nil
	}
	return base, s.FableCapable
}

// scopedNamesFor is every per-model window name an account reports for one of
// the model families, sorted.
func scopedNamesFor(s *store.Store, families map[string]bool) []string {
	names := map[string]bool{}
	for _, entry := range UsageEntriesByAccount(s, map[string]bool{}) {
		if u := oauth.NewUsage(entry.LastGood); u != nil {
			for _, w := range u.Scoped {
				for family := range families {
					if strings.Contains(strings.ToLower(w.Name), family) {
						names[w.Name] = true
					}
				}
			}
		}
	}
	models := make([]string, 0, len(names))
	for name := range names {
		models = append(models, name)
	}
	sort.Strings(models)
	return models
}
