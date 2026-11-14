package lifecycle

import "github.com/tyclab/tycswap/internal/store"

func SetStartLogin(s *store.Store, identifier string, allowed bool) error {
	return s.WithRosterLocked(func(data *store.SequenceData) error {
		num, _, _, err := s.ResolveAccountFrom(data, identifier)
		if err != nil {
			return err
		}
		rec, _ := recordAt(data, num)
		if rec.boolVal("startLogin") == allowed {
			return nil
		}
		if allowed {
			rec.set("startLogin", true)
		} else {
			rec.del("startLogin")
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
	for _, key := range []string{"alias", "disabled", "startLogin"} {
		if src.has(key) {
			dst.set(key, src.vals[key])
		}
	}
}
