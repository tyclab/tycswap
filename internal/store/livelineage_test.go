// Tests for the live_lineage.json chain record: written only when its content
// changes, and read tolerantly.
package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/oauth"
)

func TestLiveLineageWriteOnChangeOnly(t *testing.T) {
	s := freshStore(t)
	path := filepath.Join(s.BackupDir(), "live_lineage.json")
	id := &oauth.Identity{UUID: "acct-obi", Email: lineageEmail, OrgUUID: "org-five"}
	if err := s.writeLiveLineage("fp-1", "5", id); err != nil {
		t.Fatal(err)
	}
	first := mustRead(t, path)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if err := s.writeLiveLineage("fp-1", "5", id); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("an unchanged record was rewritten (mtime %v, want %v, err %v)", info.ModTime(), old, err)
	}
	if got := mustRead(t, path); string(got) != string(first) {
		t.Fatalf("record bytes changed:\n%s\nwant\n%s", got, first)
	}

	if err := s.writeLiveLineage("fp-2", "5", id); err != nil {
		t.Fatal(err)
	}
	rec, ok := s.readLiveLineage()
	if !ok || rec.Fingerprint != "fp-2" || rec.Slot != "5" || rec.Identity.UUID != "acct-obi" || rec.Identity.OrgUUID != "org-five" {
		t.Fatalf("after a change: %+v, %v; want fingerprint fp-2", rec, ok)
	}
}

func TestLiveLineageMalformedReadsAsAbsent(t *testing.T) {
	for name, body := range map[string]string{
		"not json":            "{",
		"array":               "[]",
		"wrong schema":        `{"schemaVersion":2,"fingerprint":"fp","slot":"5","identity":{"uuid":"u","email":"e","orgUuid":"o"},"seenAt":1}`,
		"no fingerprint":      `{"schemaVersion":1,"slot":"5","identity":{"uuid":"u","email":"e","orgUuid":"o"},"seenAt":1}`,
		"no slot":             `{"schemaVersion":1,"fingerprint":"fp","identity":{"uuid":"u","email":"e","orgUuid":"o"},"seenAt":1}`,
		"identity not object": `{"schemaVersion":1,"fingerprint":"fp","slot":"5","identity":"u","seenAt":1}`,
		"slot not string":     `{"schemaVersion":1,"fingerprint":"fp","slot":5,"identity":{"uuid":"u"},"seenAt":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := freshStore(t)
			if err := os.WriteFile(filepath.Join(s.BackupDir(), "live_lineage.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if rec, ok := s.readLiveLineage(); ok {
				t.Fatalf("readLiveLineage = %+v, want absent", rec)
			}
		})
	}
	s := freshStore(t)
	if _, ok := s.readLiveLineage(); ok {
		t.Fatal("a missing record read as present")
	}
}
