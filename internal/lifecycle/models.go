package lifecycle

import (
	"reflect"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/store"
)

func SetModels(s *store.Store, identifier string, set []string) error {
	return s.WithRosterLocked(func(data *store.SequenceData) error {
		num, _, _, err := s.ResolveAccountFrom(data, identifier)
		if err != nil {
			return err
		}
		want, err := store.ParseModels(strings.Join(set, ","))
		if err != nil {
			return cerr.Validation("%s", err.Error())
		}
		if len(want) == 0 {
			return cerr.Validation("choose at least one model or use --derived")
		}
		if have, ok := store.ModelsFrom(data, num); ok && reflect.DeepEqual(have, want) {
			return nil
		}
		rec, _ := recordAt(data, num)
		rec.set(store.ModelsKey, want)
		return putModels(s, data, num, rec)
	})
}

// ClearModels removes the account's models key, so its set is derived again.
func ClearModels(s *store.Store, identifier string) error {
	return s.WithRosterLocked(func(data *store.SequenceData) error {
		num, _, _, err := s.ResolveAccountFrom(data, identifier)
		if err != nil {
			return err
		}
		rec, _ := recordAt(data, num)
		if !rec.has(store.ModelsKey) {
			return nil
		}
		rec.del(store.ModelsKey)
		return putModels(s, data, num, rec)
	})
}

func putModels(s *store.Store, data *store.SequenceData, num string, rec *record) error {
	if err := putRecord(data, num, rec); err != nil {
		return err
	}
	data.LastUpdated = timestamp(s)
	return s.WriteSequence(data)
}
