// fableroom.go — room for Fable as the Fable placement rule reads it (DESIGN
// A112), and the accounts that have room but no Fable login.
package reporting

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

// ModelLimitWindows names the windows of entry's decision reading that stop
// family as of now (5h, 7d and the family's own, at 100 %) and when the last
// of them resets, zero when a reset is unknown.
func ModelLimitWindows(entry usage.UsageEntry, family string, now time.Time) ([]string, time.Time) {
	v, _ := entry.DecisionValue().(map[string]any)
	u := oauth.NewUsage(oauth.AsOf(v, now))
	return oauth.LimitingWindows(u, oauth.FamilyWindows(u, family))
}

// FableSwitchable is the Fable pool less the accounts whose explicit model
// allocation leaves Fable out: the accounts the Fable tick may switch the
// Fable store to. s is the default store.
func FableSwitchable(s *store.Store) []string {
	data, err := s.SequenceForUpdate()
	if err != nil {
		return nil
	}
	return slices.DeleteFunc(FablePool(s), func(num string) bool {
		models, explicit := store.ModelsFrom(data, num)
		return explicit && !slices.Contains(models, "fable")
	})
}

// FableRoom is the cached usage's word on room for Fable (DESIGN A112).
type FableRoom struct {
	// Room: an account the Fable store can hold has room for Fable: its live
	// account (an unknown one counts) or one the Fable tick may switch it to.
	Room bool
	// Back is the account among them whose limit ends first, at BackAt; ""
	// when no reset is known.
	Back   string
	BackAt time.Time
	// WithoutLogin, while none has room: the enabled, eligible accounts able to
	// run Fable, not allocated away from it, with room and no Fable login.
	WithoutLogin []string
}

// ReadFableRoom reads the roster, the Fable store's state and the cached usage
// only: room is no ModelLimitWindows for Fable, also without a reading. A
// store it cannot read has room. s is the default store.
func ReadFableRoom(s *store.Store) FableRoom {
	data, err := s.SequenceForUpdate()
	if _, ferr := s.ForFable(); err != nil || ferr != nil || s.IsFable() {
		return FableRoom{Room: true}
	}
	holdable := FableSwitchable(s)
	if st := s.FableStore(); st.On && st.Usable {
		if st.Live.Slot == "" {
			return FableRoom{Room: true}
		}
		if !slices.Contains(holdable, st.Live.Slot) {
			holdable = append([]string{st.Live.Slot}, holdable...)
		}
	}
	ids := map[string]usage.Identity{}
	for num := range data.Accounts {
		rec, _ := recordFor(data, num)
		ids[num] = usage.Identity{Email: recStr(rec, "email"), OrgUUID: recStr(rec, "organizationUuid")}
	}
	entries, now := s.Usage.Entries(ids), s.Clk.Now()
	room := func(num string) (bool, time.Time) {
		limits, back := ModelLimitWindows(entries[num], "fable", now)
		return len(limits) == 0, back
	}
	var out FableRoom
	for _, num := range holdable {
		ok, back := room(num)
		if ok {
			return FableRoom{Room: true}
		}
		if !back.IsZero() && (out.Back == "" || back.Before(out.BackAt)) {
			out.Back, out.BackAt = num, back
		}
	}
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if s.HasFableLogin(num) || !s.RotationEligible(data, num) || !s.FableCapable(num) {
			continue
		}
		if models, explicit := store.ModelsFrom(data, num); explicit && !slices.Contains(models, "fable") {
			continue
		}
		if ok, _ := room(num); ok {
			out.WithoutLogin = append(out.WithoutLogin, num)
		}
	}
	return out
}

// FableRoomHint names accounts with Fable room but no Fable login, each with
// the command that signs it in for Fable.
func FableRoomHint(nums []string) string {
	parts := make([]string, len(nums))
	for i, num := range nums {
		parts[i] = "Account-" + num + " has Fable room but no Fable login: tycswap add --login --lineage fable --slot " + num
	}
	return strings.Join(parts, "; ")
}

// fableRoomWithoutLogin is what status and list add while no account the
// Fable store can hold has room: the numbers of FableRoom.WithoutLogin for
// JSON and their human line; nil and "" otherwise.
func fableRoomWithoutLogin(s *store.Store) ([]int, string) {
	room := ReadFableRoom(s)
	if room.Room || len(room.WithoutLogin) == 0 {
		return nil, ""
	}
	nums := make([]int, 0, len(room.WithoutLogin))
	for _, num := range room.WithoutLogin {
		n, _ := strconv.Atoi(num)
		nums = append(nums, n)
	}
	return nums, printer.Yellowed(FableRoomHint(room.WithoutLogin))
}
