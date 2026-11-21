package reporting

import (
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
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
// itself, which follows no session. mythos is the accounts marked for Mythos
// while a Mythos session runs; Mythos in unservable means they have no room,
// or there are none (A106). fableOnDefault is the live Fable sessions on the
// default login the policy does not follow (A108).
func CountingCaption(hosted bool, models, sessions, unservable, reserved, mythos []string, fableOnDefault int) string {
	line := "counting 5h + 7d"
	for _, m := range models {
		line += " + " + m
	}
	if hosted {
		return line + " (the engine hosted here does not follow sessions)"
	}
	if len(models) > 0 && !containsFold(sessions, "mythos") {
		line += " (live " + strings.Join(models, ", ") + " sessions)"
	}
	var windows []string
	for _, name := range unservable {
		if name != MythosModel {
			windows = append(windows, name)
		}
	}
	if len(windows) > 0 {
		line += "; " + strings.Join(windows, ", ") + " not counted: no usable account can serve it"
	}
	switch {
	case len(windows) < len(unservable) && len(mythos) == 0:
		line += "; " + MythosModel + " not served: no account marked"
	case len(windows) < len(unservable):
		line += "; " + MythosModel + " not served: marked accounts " + strings.Join(mythos, ", ") + " have no room"
	case len(mythos) > 0:
		line += "; live " + MythosModel + " session: only accounts " + strings.Join(mythos, ", ")
	}
	switch {
	case fableOnDefault > 0:
		line += "; " + strconv.Itoa(fableOnDefault) + " Fable session"
		if fableOnDefault > 1 {
			line += "s"
		}
		line += " on the default login not followed (start --check names it)" + reservedSuffix(reserved)
	case len(sessions) == 0:
		line += "; no live session has reported its model"
	case !containsFold(sessions, "fable"):
		line += "; no live Fable session" + reservedSuffix(reserved)
	}
	return line
}

func reservedSuffix(reserved []string) string {
	if len(reserved) == 0 {
		return ""
	}
	return " — accounts with Fable room reserved: " + strings.Join(reserved, ", ")
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// CountedForSwitch is what a switch by strategy counts and ranks on: the
// model windows the default-session policy counts, with its caption (DESIGN
// A98), the saved settings, the policy's reservation and the quarantined
// accounts (DESIGN A99).
func CountedForSwitch(s *store.Store) switching.Counting {
	set := settings.Load(s.BackupDir())
	r := runDefaultPolicy(s, set, s.Clk.Now())
	models := settings.ParseModelNames(r.Settings.Model)
	quarantined := map[string]bool{}
	for num := range autoswitch.ReadQuarantineEntries(autoswitch.StatePath(s.BackupDir())) {
		quarantined[num] = true
	}
	return switching.Counting{Models: models, Caption: CountingCaption(false, r.CountedWindows, r.Sessions, r.Unservable, r.ReservedIn(s), r.Mythos, r.FableOnDefault),
		Settings: set, Reserved: r.Reserved, Quarantined: quarantined, Serves: r.Serves}
}
