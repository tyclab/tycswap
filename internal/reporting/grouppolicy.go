package reporting

import (
	"sort"
	"strings"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

func GroupSettings(s *store.Store, base settings.AutoSwitchSettings) settings.AutoSwitchSettings {
	base = settings.LoadOver(s.ScopeRoot(), base)
	base.Model = nil
	families := map[string]bool{}
	if state, err := recovery.NewStore(s.BackupDir()).Snapshot(); err == nil {
		sessions, _, _ := procdetect.GetRunningInstancesErr(s.ProfileDir())
		for _, session := range sessions {
			model := strings.ToLower(state.Sessions[session.SessionID].Event.Model)
			for _, family := range []string{"fable", "opus", "sonnet", "haiku"} {
				if groups.CheckModel(s.GroupID(), model) == nil && (model == family || strings.HasPrefix(model, "claude-"+family+"-")) {
					families[family] = true
				}
			}
		}
	}
	if len(families) == 0 {
		families[string(s.GroupID())] = true
	}
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
	var models []string
	for name := range names {
		models = append(models, name)
	}
	sort.Strings(models)
	model := strings.Join(models, ",")
	if model == "" && s.GroupID() == groups.Fable {
		model = "Fable"
	}
	if model != "" {
		base.Model = &model
	}
	return base
}
