package lifecycle

import "github.com/tyclab/tycswap/internal/store"

func SetStartLogin(s *store.Store, identifier string, allowed bool) error {
	return setMark(s, identifier, "startLogin", allowed)
}

// SetMythos marks or unmarks an account as one that runs Claude Mythos
// (DESIGN A106), under the roster lock like the start login's mark.
func SetMythos(s *store.Store, identifier string, allowed bool) error {
	return setMark(s, identifier, "mythos", allowed)
}

// setMark writes the operator's boolean mark key on the account identifier
// names: true sets it, false removes it, and an unchanged mark writes nothing.
func setMark(s *store.Store, identifier, key string, allowed bool) error {
	return s.WithRosterLocked(func(data *store.SequenceData) error {
		num, _, _, err := s.ResolveAccountFrom(data, identifier)
		if err != nil {
			return err
		}
		rec, _ := recordAt(data, num)
		if rec.boolVal(key) == allowed {
			return nil
		}
		if allowed {
			rec.set(key, true)
		} else {
			rec.del(key)
		}
		if err := putRecord(data, num, rec); err != nil {
			return err
		}
		data.LastUpdated = timestamp(s)
		return s.WriteSequence(data)
	})
}

func accountMetadata(s *store.Store, data *store.SequenceData, email, org string) *record {
	num := s.FindAccountSlot(data, email, org)
	rec, _ := recordAt(data, num)
	return rec
}

func carryAccountMetadata(dst, src *record) {
	if src == nil {
		return
	}
	for _, key := range []string{"alias", "disabled", "startLogin", "mythos", store.FableMarkKey, store.ModelsKey} {
		if src.has(key) {
			dst.set(key, src.vals[key])
		}
	}
}
