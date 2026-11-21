//go:build linux

package reporting

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/settings"
)

const linkSessionID = "64021ad6-58a5-4b99-84e5-b03d89d3047d"

func linkFixture(t *testing.T) (root, link, source, dest string) {
	t.Helper()
	s := newStore(t, nil, nil)
	root, source, dest = s.BackupDir(), s.DefaultProfileDir(), paths.FableStoreDir(s.BackupDir())
	writeActiveCreds(t, s, oauthCreds("original", "source", 0))
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".credentials.json"), []byte(oauthCreds("second", "target", 0)), 0600); err != nil {
		t.Fatal(err)
	}
	link, err := CreateSessionLink(root, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := BindSessionLink(root, linkSessionID, link, "opus", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	return
}

func TestClientRefreshAndRacingObserver(t *testing.T) {
	root, link, source, dest := linkFixture(t)
	clientLock := filepath.Join(link, ".oauth_refresh.lock")
	if err := os.Mkdir(clientLock, 0700); err != nil {
		t.Fatal(err)
	}
	// A live client's outer lock and owner file outlast its credential write.
	owner := clientLock + ".owner"
	if err := os.WriteFile(owner, []byte("simulated client"), 0600); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	entered := make(chan struct{})
	go func() {
		first <- UpdateSessionLink(root, linkSessionID, link, time.Now(), func(s *SessionLink) error {
			s.Family, s.Target = "fable", dest
			close(entered)
			return nil
		})
	}()
	<-entered
	second := make(chan error, 1)
	go func() {
		second <- UpdateSessionLink(root, linkSessionID, link, time.Now(), func(s *SessionLink) error {
			if s.Target != dest || s.Family != "fable" {
				return errors.New("racing observer saw stale binding")
			}
			return nil
		})
	}()
	select {
	case err := <-first:
		t.Fatalf("move finished while client held refresh lock: %v", err)
	case <-time.After(35 * time.Millisecond):
	}
	if target, err := os.Readlink(link); err != nil || target != source {
		t.Fatalf("client was redirected before write: %q, %v", target, err)
	}
	replacement := oauthCreds("replacement", "rotated", 0)
	if err := os.WriteFile(filepath.Join(link, ".credentials.json"), []byte(replacement), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{owner, clientLock} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, done := range []chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]string{source: replacement, dest: oauthCreds("second", "target", 0)} {
		got, err := os.ReadFile(filepath.Join(path, ".credentials.json"))
		if err != nil || string(got) != want {
			t.Fatalf("refresh wrote the wrong login in %s", path)
		}
		if _, err := os.Stat(filepath.Join(path, ".oauth_refresh.lock")); !os.IsNotExist(err) {
			t.Fatalf("residual refresh lock in %s: %v", path, err)
		}
	}
}

func TestClientArrivingDuringMoveReadsDestination(t *testing.T) {
	_, link, source, dest := linkFixture(t)
	lock := filepath.Join(link, ".oauth_refresh.lock")
	result := make(chan error, 1)
	firstAttempt := make(chan struct{})
	err := withSessionRefreshLock(source, time.Second, func() error {
		go func() {
			if err := os.Mkdir(lock, 0700); !os.IsExist(err) {
				close(firstAttempt)
				result <- errors.New("client did not encounter mover's refresh lock")
				return
			}
			close(firstAttempt)
			for {
				err := os.Mkdir(lock, 0700)
				if !os.IsExist(err) {
					if err == nil {
						buf, readErr := os.ReadFile(filepath.Join(link, ".credentials.json"))
						if readErr != nil || string(buf) != oauthCreds("second", "target", 0) {
							err = errors.New("client reread the wrong login")
						}
						err = errors.Join(err, os.Remove(lock))
					}
					result <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		<-firstAttempt
		if err := os.Symlink(dest, link+".new"); err != nil {
			return err
		}
		return os.Rename(link+".new", link)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestSessionMoveRejectsIncompleteBindingAndForeignTarget(t *testing.T) {
	root, link, source, _ := linkFixture(t)
	bad := t.TempDir()
	mutate := func(rec *SessionLink) error { rec.Target = bad; return nil }
	if err := UpdateSessionLink(root, linkSessionID, link, time.Now(), mutate); err == nil {
		t.Fatal("accepted arbitrary login folder")
	}
	record := filepath.Join(root, defaultModelsFile)
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"target", "token", "pid", "procStart"} {
		var doc struct {
			Sessions map[string]map[string]any `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		delete(doc.Sessions[linkSessionID]["link"].(map[string]any), key)
		broken, _ := json.Marshal(doc)
		if err := os.WriteFile(record, broken, 0600); err != nil {
			t.Fatal(err)
		}
		if err := UpdateSessionLink(root, linkSessionID, link, time.Now(), func(*SessionLink) error {
			t.Fatal("accepted binding without " + key)
			return nil
		}); err == nil {
			t.Fatal("omission passed: " + key)
		}
		if target, err := os.Readlink(link); err != nil || target != source {
			t.Fatal("moved incomplete binding")
		}
	}
}

func TestSessionPrunePreservesLiveLinkAndResume(t *testing.T) {
	if root := os.Getenv("TYCSWAP_TEST_CREATE_LINK_ROOT"); root != "" {
		link, err := CreateSessionLink(root, paths.GetClaudeConfigHome())
		if err != nil {
			t.Fatal(err)
		}
		if err := BindSessionLink(root, "6135d791-d1e9-40e7-82d7-b3b819989fcf", link, "opus", "", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateSessionLink(root, paths.GetClaudeConfigHome()); err != nil {
			t.Fatal(err)
		}
		return
	}
	root, link, _, _ := linkFixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionPrunePreservesLiveLinkAndResume$")
	cmd.Env = append(os.Environ(), "TYCSWAP_TEST_CREATE_LINK_ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %s: %v", out, err)
	}
	if err := PruneSessionLinks(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(link))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(link) {
		t.Fatalf("after prune: %v, %v", entries, err)
	}
	saved, ok, err := LookupSessionLink(root, "6135d791-d1e9-40e7-82d7-b3b819989fcf")
	if err != nil || !ok || !strings.HasSuffix(saved.Target, "/.claude") {
		t.Fatalf("lost resume: %+v, %v", saved, err)
	}
	const resumed = "6135d791-d1e9-40e7-82d7-b3b819989fcf"
	news, err := RecordDefaultSessionModel(root, resumed, "haiku", "", time.Now())
	if !news || err != nil {
		t.Fatalf("unlinked resume observation: news=%v, %v", news, err)
	}
	if _, linked, err := LookupSessionLink(root, resumed); linked || err != nil {
		t.Fatalf("unlinked resume retained an ended link: %v", err)
	}

}

func TestSharedMythosStartAndUnplacedSwitchHaveDifferentEffectsOnOtherSessions(t *testing.T) {
	for _, initial := range []string{"mythos", "opus"} {
		t.Run(initial, func(t *testing.T) {
			s, now := (incident{live: "3", sessions: map[string]string{linkSessionID: initial}}).store(t)
			markMythos("8")(t, s)
			link, err := CreateSessionLink(s.BackupDir(), s.DefaultProfileDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := BindSessionLink(s.BackupDir(), linkSessionID, link, initial, "", now); err != nil {
				t.Fatal(err)
			}
			if err := UpdateSessionLink(s.BackupDir(), linkSessionID, link, now, func(rec *SessionLink) error { rec.Family = "mythos"; return nil }); err != nil {
				t.Fatal(err)
			}
			policy := DefaultSessionPolicyResult(s, settings.Default(), now)
			if (policy.Serves != nil) != (initial == "mythos") {
				t.Fatalf("shared login restriction for a %s start: %v", initial, policy.Serves != nil)
			}
		})
	}
}

func TestInterruptedMoveUsesTheRecordForTheVisibleLink(t *testing.T) {
	root, path, source, dest := linkFixture(t)
	records, err := loadDefaultSessionModels(root)
	if err != nil {
		t.Fatal(err)
	}
	before := records[linkSessionID]
	after := before
	link := *before.Link
	link.Target, link.Wanted = dest, "fable"
	after.Link, after.Before, after.Store = &link, &before, "fable"
	records[linkSessionID] = after
	if err := saveSessionModels(root, records); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{source, dest} {
		if want == dest {
			if err := os.Symlink(dest, path+".new"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".new", path); err != nil {
				t.Fatal(err)
			}
		}
		got, ok, err := LookupSessionLink(root, linkSessionID)
		if err != nil || !ok || got.Target != want || (got.Wanted == "fable") != (want == dest) {
			t.Fatalf("interrupted move read %+v, %v; want %s", got, err, want)
		}
	}
}
