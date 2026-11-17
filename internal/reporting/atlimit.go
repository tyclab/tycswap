// atlimit.go (Go-side, DESIGN A15): an exhausted per-model weekly window reads at-limit even when 7d has room; it uses the
// same oauth projection the switch decisions use, so a marker never disagrees with a pick.

package reporting

import (
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

// configuredModels is the models the default-session policy counts (DESIGN
// A91, A97).
func configuredModels(s *store.Store) []string {
	return settings.ParseModelNames(runDefaultPolicy(s, settings.Load(s.BackupDir()), s.Clk.Now()).Settings.Model)
}

// runDefaultPolicy is the policy configuredModels runs; a variable so a test
// can count the runs.
var runDefaultPolicy = DefaultSessionPolicyResult

// Unknown headroom (sentinel, nil, non-map) is never at-limit; windows read as of now, like the engine's (DESIGN A91).
func atLimitFor(decisionValue any, models []string, now time.Time) (bool, []string) {
	m, ok := decisionValue.(map[string]any)
	if !ok {
		return false, nil
	}
	u := oauth.NewUsage(oauth.AsOf(m, now))
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
func atLimitMarker(decisionValue any, models []string, now time.Time) string {
	atLimit, windows := atLimitFor(decisionValue, models, now)
	if !atLimit {
		return ""
	}
	return printer.Yellowed("at limit: " + strings.Join(windows, ", "))
}
