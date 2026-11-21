package reporting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/store"
)

func TestBadSessionRecordPreservesHealthySiblings(t *testing.T) {
	for _, invalid := range []string{`{"family":"opus","seenAt":1,"store":"mythos/abc","link":42}`, `{"family":"mythos","seenAt":1,"link":{"target":"bad"}}`} {
		t.Run(invalid, func(t *testing.T) {
			root, link, _, _ := linkFixture(t)
			const badID = "d0954fbe-75c1-4873-8921-6bf4745047a3"
			p := filepath.Join(root, defaultModelsFile)
			var doc struct {
				Sessions map[string]json.RawMessage `json:"sessions"`
			}
			read := func() {
				t.Helper()
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
			}
			read()
			doc.Sessions[badID] = json.RawMessage(invalid)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(p, raw, 0600); err != nil {
				t.Fatal(err)
			}
			live, err := LinkedSessions(root)
			if err != nil || len(live) != 1 || live[0].ID != linkSessionID {
				t.Errorf("one invalid record hid the live sibling: %v %v", live, err)
			}
			err = UpdateSessionLink(root, linkSessionID, link, time.Now(), func(s *SessionLink) error { s.Family = "sonnet"; return nil })
			if err != nil {
				t.Errorf("could not update healthy sibling: %v", err)
			}
			if err = RecordSessionStart(root, "other", "opus", "", time.Now()); err != nil {
				t.Errorf("could not record another session: %v", err)
			}
			if _, err = SessionBinding(root, badID); err == nil {
				t.Error("a damaged identity became a valid resume")
			}
			if _, _, err = LookupSessionLink(root, badID); err == nil {
				t.Error("a damaged binding became an absent link")
			}
			if _, _, ok := RecordedSession(root, badID); ok {
				t.Error("reported unusable model evidence")
			}
			read()
			if string(doc.Sessions[badID]) != invalid {
				t.Errorf("damaged record was altered: %s", doc.Sessions[badID])
			}
		})
	}
}

func TestMissingPendingLinkIsAnEndedSession(t *testing.T) {
	root, path, _, dest := linkFixture(t)
	records, err := loadDefaultSessionModels(root)
	if err != nil {
		t.Fatal(err)
	}
	old := records[linkSessionID]
	digest := strings.Repeat("1", 64)
	old.Store, old.Link.Target, old.Family = "mythos/"+digest, filepath.Join(store.MythosLoginRoot(root), digest), "mythos"
	rec := old
	link := *old.Link
	link.Target = dest
	rec.Link, rec.Before, rec.Store = &link, &old, "fable"
	records[linkSessionID] = rec
	if err = saveSessionModels(root, records); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	live, err := LinkedSessions(root)
	if err != nil || len(live) != 0 {
		t.Fatalf("missing link should resolve to ended: %+v %v", live, err)
	}
	if got, err := SessionBinding(root, linkSessionID); err != nil || got != old.Store {
		t.Fatalf("lost last confirmed resume identity: %q %v", got, err)
	}
	if err = PruneSessionLinks(root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, defaultModelsFile))
	if err != nil || strings.Contains(string(raw), `"beforeMove"`) {
		t.Fatalf("pending move remained after write: %s %v", raw, err)
	}
}

func TestEndedLinkedRecordRetention(t *testing.T) {
	root, _, source, dest := linkFixture(t)
	records, err := loadDefaultSessionModels(root)
	if err != nil {
		t.Fatal(err)
	}
	old := records[linkSessionID]
	old.SeenAt = float64(time.Now().Add(-48 * time.Hour).Unix())
	records[linkSessionID] = old
	digest := strings.Repeat("f", 64)
	for i, tc := range []struct{ id, target, scope, binding, resume string }{
		{"ended-default", source, "", "", ""},
		{"ended-fable", dest, "fable", "", ""},
		{"ended-mythos", filepath.Join(store.MythosLoginRoot(root), digest), "mythos/" + digest, "", ""},
		{"ended-recovery", dest, "fable", "fable/" + digest, ""},
		{"ended-resume", source, "", "", digest},
		{"recent", source, "", "", ""},
	} {
		rec := old
		link := *old.Link
		link.PID = os.Getpid()
		link.ProcStart = "1"
		link.Token = fmt.Sprintf("%d-1-%032x", os.Getpid(), i)
		link.Target, link.ResumeMythos = tc.target, tc.resume
		rec.Link, rec.Store, rec.Binding = &link, tc.scope, tc.binding
		if tc.resume != "" {
			rec.Family = "mythos"
		}
		if tc.id == "recent" {
			rec.SeenAt = float64(time.Now().Unix())
		}
		records[tc.id] = rec
	}
	if err = saveSessionModels(root, records); err != nil {
		t.Fatal(err)
	}
	if err = PruneSessionLinks(root); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ended-default", "ended-fable"} {
		if _, _, ok := RecordedSession(root, id); ok {
			t.Errorf("expired %s still recorded", id)
		}
	}
	for _, id := range []string{"recent", linkSessionID, "ended-mythos", "ended-recovery", "ended-resume"} {
		if _, _, ok := RecordedSession(root, id); !ok {
			t.Errorf("prematurely removed %s", id)
		}
	}
	rec, ok, err := LookupSessionLink(root, "ended-resume")
	if !ok || err != nil || rec.ResumeMythos != digest {
		t.Errorf("lost identity a pending resume needs: %+v %v", rec, err)
	}
}

func TestPruneRemovesOnlyEndedSessionQuestionFiles(t *testing.T) {
	root, active, _, _ := linkFixture(t)
	dir := filepath.Dir(active)
	live := filepath.Join(dir, "."+filepath.Base(active)+".ask")
	dead := filepath.Join(dir, fmt.Sprintf(".%d-1-%032x.ask", os.Getpid(), 3))
	unrelated := filepath.Join(dir, ".unknown.ask")
	for _, name := range []string{live, dead, unrelated} {
		if err := os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneSessionLinks(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("ended session's question lock remains")
	}
	for _, name := range []string{live, unrelated} {
		if _, err := os.Stat(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPruneExpiresEndedRecordsAfterLinksDirectoryIsRemoved(t *testing.T) {
	root, path, _, _ := linkFixture(t)
	records, err := loadDefaultSessionModels(root)
	if err != nil {
		t.Fatal(err)
	}
	rec := records[linkSessionID]
	rec.SeenAt = float64(time.Now().Add(-48 * time.Hour).Unix())
	rec.Link.ProcStart = "1"
	rec.Link.Token = fmt.Sprintf("%d-1-%032x", rec.Link.PID, 7)
	records[linkSessionID] = rec
	if err = saveSessionModels(root, records); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err = os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err = PruneSessionLinks(root); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := RecordedSession(root, linkSessionID); ok {
		t.Error("ended shared session still recorded after links directory went away")
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Error("pruning recreated the links directory")
	}
}
