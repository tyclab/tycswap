package store

func (s *Store) WithRosterLocked(fn func(*SequenceData) error) error {
	return s.Lock.With(func() error {
		data, err := s.MigratedSequenceForUpdateLocked()
		if err != nil {
			return err
		}
		return fn(data)
	})
}
