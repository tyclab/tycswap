package reporting

import (
	"strings"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

func configuredModels(s *store.Store) []string {
	return settings.ParseModelNames(settings.Load(s.BackupDir()).Model)
}

func atLimitFor(decisionValue any, models []string) (bool, []string) {
	m, ok := decisionValue.(map[string]any)
	if !ok {
		return false, nil
	}
	u := oauth.NewUsage(m)
	h := oauth.AccountHeadroom(u, models)
	if h == nil || *h > 0 {
		return false, nil
	}
	var limiting []string
	for _, w := range oauth.RelevantWindows(u, models) {
		if w.Pct >= 100 {
			limiting = append(limiting, w.Label)
		}
	}
	return true, limiting
}

// atLimitMarker returns the styled human marker ("at limit: 7d, Fable 5") for an
// at-limit account, or "" otherwise. It is placed beside the account's other
// row markers ((active)/(disabled)) and styled in the printer's warning color,
// matching the surrounding attention markers.
func atLimitMarker(decisionValue any, models []string) string {
	atLimit, windows := atLimitFor(decisionValue, models)
	if !atLimit {
		return ""
	}
	return printer.Yellowed("at limit: " + strings.Join(windows, ", "))
}
