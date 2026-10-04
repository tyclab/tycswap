package store

import (
	"errors"
	"testing"
	"time"
)

func TestOtherGoroutineCannotBorrowTransaction(t *testing.T) {
	for _, operation := range []string{"alias", "snapshot", "delete-snapshot", "transaction"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			s := f.open()
			mustUpsert(t, s, keyA, "a@example.com", "pro")
			holder := s.Lock()
			if ok, err := holder.Acquire(time.Second); err != nil || !ok {
				t.Fatalf("acquire: %v %v", ok, err)
			}
			defer holder.Release()
			done := make(chan error, 1)
			go func() {
				other := f.open()
				switch operation {
				case "alias":
					done <- other.SetAlias(keyA, "work")
				case "snapshot":
					done <- other.WriteSnapshot(keyA, map[string]any{"fixture": true})
				case "delete-snapshot":
					done <- other.DeleteSnapshot(keyA)
				case "transaction":
					done <- other.WithLock(func(tx *Store) error { return tx.SetDisabled(keyA, true) })
				}
			}()
			select {
			case err := <-done:
				t.Fatalf("write entered another transaction: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			// The owner's scoped view can still write without reacquiring.
			if err := holder.Store().SetActive(keyA); err != nil {
				t.Fatal(err)
			}
			holder.Release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("writer stayed blocked after release")
			}
		})
	}
}

func TestReleasedTransactionCannotWrite(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	var escaped *Store
	if err := s.WithLock(func(tx *Store) error { escaped = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, write := range []func() error{
		func() error { return escaped.SetActive(keyA) },
		func() error { return escaped.WriteSnapshot(keyA, map[string]any{"fixture": true}) },
		func() error { return escaped.DeleteSnapshot(keyA) },
		func() error {
			return escaped.WithLock(func(*Store) error { t.Error("closed callback ran"); return nil })
		},
	} {
		if err := write(); !errors.Is(err, ErrTransactionClosed) {
			t.Fatalf("expected closed transaction, got %v", err)
		}
	}
}
