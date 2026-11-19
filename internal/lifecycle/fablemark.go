package lifecycle

import (
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/store"
)

// SetFableMark writes the account's Fable mark, or removes it so the plan
// decides again, under the roster lock (DESIGN A107). A mark that cannot
// change the answer is refused: an API-key account, and a plan without Fable.
func SetFableMark(s *store.Store, identifier string, allowed bool) error {
	return s.WithRosterLocked(func(data *store.SequenceData) error {
		num, _, _, err := s.ResolveAccountFrom(data, identifier)
		if err != nil {
			return err
		}
		rec, _ := recordAt(data, num)
		if allowed {
			switch a := s.FableEntitlement(data, num); {
			case a.By == store.FableAPIKey:
				return cerr.Validation("Account-%s is an API-key account; a Fable mark is for OAuth logins only.", num)
			case a.By == store.FableByPlan && !a.Capable:
				return cerr.Validation("Account-%s's plan (%s) cannot run Fable; nothing marked.", num, a.Plan)
			}
			if rec.boolVal(store.FableMarkKey) {
				return nil
			}
			rec.set(store.FableMarkKey, true)
		} else {
			if !rec.has(store.FableMarkKey) {
				return nil
			}
			rec.del(store.FableMarkKey)
		}
		if err := putRecord(data, num, rec); err != nil {
			return err
		}
		data.LastUpdated = timestamp(s)
		return s.WriteSequence(data)
	})
}

// ErrFableNotStored refuses a Fable sign-in of an identity with no OAuth
// roster account, an API-key account's included.
func ErrFableNotStored(email string) error {
	return cerr.Validation("Account %s is not stored yet: add it first with tycswap add --login.", email)
}

// FablePlanRefusal is the Fable sign-in's refusal of an OAuth account that is
// not known to run Fable, nil when it is (or for an API key, which the
// sign-in refuses on its own). It names the mark when the mark would help.
func FablePlanRefusal(s *store.Store, data *store.SequenceData, num string) error {
	a := s.FableEntitlement(data, num)
	switch {
	case a.Capable || a.By == store.FableAPIKey:
		return nil
	case a.By == store.FableByPlan:
		return cerr.Validation("Account-%s's plan does not offer Fable.", num)
	}
	return cerr.Validation("Account-%s's plan does not offer Fable. If it runs Fable, mark it first: tycswap fable --allow %s", num, num)
}
