// fableroom.go — room for Fable as the Fable placement rule reads it (DESIGN
// A112), the accounts that have room but no Fable login, and those that need
// a Fable sign-in (A115).
package reporting

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
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
	s = s.OneRead()
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
	s = s.OneRead()
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
		if s.HasFableLogin(num) || !fableServable(s, data, num) {
			continue
		}
		if ok, _ := room(num); ok {
			out.WithoutLogin = append(out.WithoutLogin, num)
		}
	}
	return out
}

// fableServable: num is enabled, rotation-eligible, able to run Fable and not
// allocated away from it by an explicit model allocation (DESIGN A112).
func fableServable(s *store.Store, data *store.SequenceData, num string) bool {
	if !s.RotationEligible(data, num) || !s.FableCapable(num) {
		return false
	}
	models, explicit := store.ModelsFrom(data, num)
	return !explicit || slices.Contains(models, "fable")
}

// NoUsableFableLogin: num holds no Fable login, or one that reads "sign in
// again": quarantine, FableQuarantine's answer, holds it (DESIGN A114).
func NoUsableFableLogin(s *store.Store, quarantine map[string]autoswitch.QuarantineEntry, num string) bool {
	_, dead := quarantine[num]
	return dead || !s.HasFableLogin(num)
}

// FableSignInNeeded is, in roster order, the fableServable accounts with
// NoUsableFableLogin, none unless an account has a Fable login (DESIGN A115).
// It reads roster, Fable logins and quarantine only. s is the default store.
func FableSignInNeeded(s *store.Store) []string {
	s = s.OneRead()
	data, _ := s.ReadSequence()
	if data == nil || s.IsFable() || !s.HasFableLogins() {
		return nil
	}
	if _, err := s.ForFable(); err != nil {
		return nil
	}
	dead := FableQuarantine(s)
	var out []string
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if fableServable(s, data, num) && NoUsableFableLogin(s, dead, num) {
			out = append(out, num)
		}
	}
	return out
}

// FableRoomHint names accounts with Fable room but no Fable login and the
// command that signs them in for Fable, one after another (DESIGN A114).
func FableRoomHint(nums []string) string {
	return accountsHint(nums, "has", "have", " Fable room but no Fable login: tycswap fable --login")
}

// FableSignInHint names the accounts that need a Fable sign-in (DESIGN A115).
func FableSignInHint(nums []string) string {
	return accountsHint(nums, "needs", "need", " a Fable sign-in: tycswap fable --login")
}

// accountsHint is nums with verb one or several, then rest; "" for none.
func accountsHint(nums []string, one, several, rest string) string {
	switch len(nums) {
	case 0:
		return ""
	case 1:
		return "Account-" + nums[0] + " " + one + rest
	}
	last := len(nums) - 1
	return "Accounts " + strings.Join(nums[:last], ", ") + " and " + nums[last] + " " + several + rest
}

// accountNumbers is nums as JSON carries account numbers; nil for none.
func accountNumbers(nums []string) []int {
	if len(nums) == 0 {
		return nil
	}
	out := make([]int, 0, len(nums))
	for _, num := range nums {
		n, _ := strconv.Atoi(num)
		out = append(out, n)
	}
	return out
}

// fableRoomWithoutLogin is what status and list add while no account the
// Fable store can hold has room: the numbers of FableRoom.WithoutLogin for
// JSON and their human line; nil and "" otherwise.
func fableRoomWithoutLogin(s *store.Store) ([]int, string) {
	room := ReadFableRoom(s)
	if room.Room || len(room.WithoutLogin) == 0 {
		return nil, ""
	}
	return accountNumbers(room.WithoutLogin), printer.Yellowed(FableRoomHint(room.WithoutLogin))
}

// fableLoginLine is status's and list's line for accounts without a usable
// Fable login: A112's hint, else A115's sentence; "" for neither.
func fableLoginLine(s *store.Store) string {
	if _, hint := fableRoomWithoutLogin(s); hint != "" {
		return hint
	}
	if hint := FableSignInHint(FableSignInNeeded(s)); hint != "" {
		return printer.Yellowed(hint)
	}
	return ""
}
