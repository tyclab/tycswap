package reporting

import (
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

// ReservedIn lists, in sequence order, the accounts r leaves to Fable work.
func (r PolicyResult) ReservedIn(s *store.Store) []string {
	out := []string{}
	if r.Reserved == nil {
		return out
	}
	if data, _ := s.ReadSequence(); data != nil {
		for _, n := range data.Sequence {
			if num := strconv.Itoa(n); r.Reserved(num) {
				out = append(out, num)
			}
		}
	}
	return out
}

// CountingCaption is the one line the dashboard, the tray and a switch by
// strategy show for what is counted, e.g. "counting 5h + 7d + Fable (live
// Fable sessions)" (DESIGN A91, A98); hosted is an engine the dashboard runs
// itself, which follows no session.
func CountingCaption(hosted bool, models, sessions, unservable, reserved []string) string {
	line := "counting 5h + 7d"
	for _, m := range models {
		line += " + " + m
	}
	if hosted {
		return line + " (the engine hosted here does not follow sessions)"
	}
	if len(models) > 0 {
		line += " (live " + strings.Join(models, ", ") + " sessions)"
	}
	if len(unservable) > 0 {
		line += "; " + strings.Join(unservable, ", ") + " not counted: no usable account can serve it"
	}
	switch {
	case len(sessions) == 0:
		line += "; no live session has reported its model"
	case !containsFold(sessions, "fable"):
		line += "; no live Fable session"
		if len(reserved) > 0 {
			line += " — accounts with Fable room reserved: " + strings.Join(reserved, ", ")
		}
	}
	return line
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// CountedForSwitch is what a switch by strategy counts: the model windows the
// default-session policy counts, with its caption (DESIGN A98).
func CountedForSwitch(s *store.Store) ([]string, string) {
	r := runDefaultPolicy(s, settings.Load(s.BackupDir()), s.Clk.Now())
	models := settings.ParseModelNames(r.Settings.Model)
	return models, CountingCaption(false, models, r.Sessions, r.Unservable, r.ReservedIn(s))
}
