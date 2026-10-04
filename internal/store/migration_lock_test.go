package store

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/filelock"
)

func TestMigrationRefusesToWriteWhileStoreLocked(t *testing.T) {
	s := freshStore(t)
	writeSequenceRaw(t, s, `{"sequence":[1],"accounts":{"1":{"email":"test@example.com"}}}`)
	before, err := os.ReadFile(s.SequenceFile)
	if err != nil {
		t.Fatal(err)
	}
	holder := filelock.New(s.LockFile, time.Second)
	if ok, err := holder.Acquire(time.Second); err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	defer holder.Release()
	s.Lock = filelock.New(s.LockFile, 100*time.Millisecond)
	if _, err := s.SequenceMigrated(); cerr.TypeName(err) != "LockError" {
		t.Fatalf("expected lock error, got %v", err)
	}
	after, err := os.ReadFile(s.SequenceFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("migration wrote through another holder's lock")
	}
}

func TestMigrationPreservesWriterChangesAfterWaiting(t *testing.T) {
	s := freshStore(t)
	writeSequenceRaw(t, s, `{"sequence":[1],"accounts":{"1":{"email":"one@example.com"}}}`)
	holder := filelock.New(s.LockFile, time.Second)
	if ok, err := holder.Acquire(time.Second); err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	defer holder.Release()
	done := make(chan error, 1)
	go func() { _, err := s.SequenceMigrated(); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("migration did not wait: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	writeSequenceRaw(t, s, `{"sequence":[1,2],"accounts":{"1":{"email":"one@example.com","alias":"edited"},"2":{"email":"two@example.com"}}}`)
	holder.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("migration did not finish")
	}
	data, err := s.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Accounts) != 2 || strField(decodeRecord(data.Accounts["1"]), "alias") != "edited" {
		t.Fatal("migration lost the writer's changes")
	}
	if needsOrgBackfill(data) {
		t.Fatal("migration was not applied")
	}
}
