package store

// One classified read inside the lock, so fn decides from the bytes it commits; overlapping spans would lose whole records.
// The caller must not hold s.Lock (non-reentrant: a deadlock), and fn must not prompt (10s cross-process budget).
func (s *Store) WithRosterLocked(fn func(*SequenceData) error) error {
	return s.Lock.With(func() error {
		data, err := s.MigratedSequenceForUpdateLocked()
		if err != nil {
			return err
		}
		return fn(data)
	})
}
