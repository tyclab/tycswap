package switching

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

func groupCreds(plan, access, refresh string) string {
	var obj map[string]any
	_ = json.Unmarshal([]byte(oauthCreds(access, refresh)), &obj)
	obj["claudeAiOauth"].(map[string]any)["subscriptionType"] = plan
	raw, _ := json.Marshal(obj)
	return string(raw)
}

func groupFixture(t *testing.T) *store.Store {
	t.Helper()
	s := newTestStore(t, nil)
	writeSeq(t, s, seqData(ptrInt(3), []int{1, 2, 3, 4}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": "one@x.com", "organizationUuid": ""}),
		"2": record(map[string]any{"email": "two@x.com", "organizationUuid": ""}),
		"3": record(map[string]any{"email": "default@x.com", "organizationUuid": ""}),
		"4": record(map[string]any{"email": "biz@x.com", "organizationUuid": "business", "fableStart": false, "fableContinue": true}),
	}))
	seedBackup(t, s, "1", "one@x.com", groupCreds("max", "a1", "r1"), "")
	seedBackup(t, s, "2", "two@x.com", groupCreds("max", "a2", "r2"), "")
	seedBackup(t, s, "3", "default@x.com", groupCreds("pro", "a3", "r3"), "")
	seedBackup(t, s, "4", "biz@x.com", groupCreds("team", "a4", "r4"), "business")
	seedLive(t, s, "default@x.com", "", groupCreds("pro", "a3", "r3"))
	return s
}

func scope(t *testing.T, s *store.Store, id groups.ID) *store.Store {
	t.Helper()
	group, err := s.ForGroup(id)
	if err != nil {
		t.Fatal(err)
	}
	return group
}

func TestGroupsIndependentAndClaimsProtectEverySurface(t *testing.T) {
	s := groupFixture(t)
	fable, opus := scope(t, s, groups.Fable), scope(t, s, groups.Opus)
	defaultBefore, _, _ := s.Creds.ReadActive()
	seqBefore, _ := os.ReadFile(s.SequenceFile)
	if _, err := SwitchTo(fable, "1", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(opus, "2", true, false); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Creds.ReadActive(); got != defaultBefore {
		t.Fatal("group activation changed the default credential")
	}
	if got, _ := os.ReadFile(s.SequenceFile); string(got) != string(seqBefore) {
		t.Fatal("group activation changed the shared active-account sequence")
	}
	if *fable.CurrentAccountNumber() != "1" || *opus.CurrentAccountNumber() != "2" {
		t.Fatal("group active accounts are not independent")
	}
	if fable.StatePath() == opus.StatePath() || fable.Usage != opus.Usage || fable.Lock != opus.Lock {
		t.Fatal("scope state/cache/lock wiring is incorrect")
	}
	for _, target := range []*store.Store{s, opus} {
		if _, err := SwitchTo(target, "1", true, false); err == nil {
			t.Fatal("another scope activated a claimed credential")
		}
	}
	if err := s.EnsureNoLiveSession("1", "one@x.com", "remove"); err == nil {
		t.Fatal("removal ignored a parked group owner")
	}
	if err := s.DeleteAccountFiles("1", "one@x.com"); err == nil {
		t.Fatal("delete ignored a group owner")
	}
	called := false
	c := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome { called = true; return oauth.RefreshOutcome{} }}
	backup, _ := s.ReadAccountCredentials("1", "one@x.com")
	if out := s.RefreshBackupGuarded(context.Background(), c, "1", "one@x.com", backup); out.Error != store.RefreshDeclined || called {
		t.Fatal("refresh rotated an owned backup")
	}
	owned, err := s.ReadOwnedCredentials("1", "one@x.com")
	if err != nil || oauth.ExtractAccessToken(owned) != "a1" {
		t.Fatalf("owner snapshot: %v", err)
	}
	if strings.Join(s.SwitchableAccountNumbers(), ",") != "3,4" {
		t.Fatalf("default selection ignored claims: %v", s.SwitchableAccountNumbers())
	}
}

func TestGroupContinuationCapability(t *testing.T) {
	s := groupFixture(t)
	fable := scope(t, s, groups.Fable)
	if _, err := SwitchTo(fable, "4", true, false); err == nil {
		t.Fatal("Business continuation capability granted startup")
	}
	if _, err := SwitchTo(fable.WithGroupIntent(groups.Continue), "4", true, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCapability("3", groups.Continue, true); err == nil {
		t.Fatal("Pro capability was granted")
	}
	if err := s.SetGroupCapability("4", groups.Continue, false); err != nil {
		t.Fatal(err)
	}
	if decision := fable.GroupCompatibility("4", groups.Continue); !decision.Known || decision.Allowed {
		t.Fatal("explicit capability denial was ignored")
	}
}

func TestTwoGroupsRaceForOneCredential(t *testing.T) {
	s := groupFixture(t)
	stores := []*store.Store{scope(t, s, groups.Fable), scope(t, s, groups.Opus)}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, group := range stores {
		wait.Add(1)
		go func(group *store.Store) {
			defer wait.Done()
			_, err := SwitchTo(group, "1", true, false)
			results <- err
		}(group)
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("%d groups activated the same rotating credential", success)
	}
	owner, err := s.CredentialOwner("1")
	if err != nil || owner.Scope == "" || owner.Uncertain {
		t.Fatalf("owner after race: %+v, %v", owner, err)
	}
}

type failingGroupCreds struct {
	credstore.Store
	rollbackFails bool
}

func (c failingGroupCreds) WriteActiveAccount(value string) error {
	if err := c.Store.WriteActiveAccount(value); err != nil {
		return err
	}
	return errors.New("injected failure after active credential write")
}

func (c failingGroupCreds) WriteActive(value string) error {
	if c.rollbackFails {
		return errors.New("injected rollback failure")
	}
	return c.Store.WriteActive(value)
}

func TestGroupPartialCredentialWriteRollsBackAndFailedRollbackHolds(t *testing.T) {
	for _, failedRollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "hold"}[failedRollback], func(t *testing.T) {
			s := groupFixture(t)
			fable := scope(t, s, groups.Fable)
			if _, err := SwitchTo(fable, "1", true, false); err != nil {
				t.Fatal(err)
			}
			original := fable.Creds
			fable.Creds = failingGroupCreds{Store: original, rollbackFails: failedRollback}
			if _, err := SwitchTo(fable, "2", true, false); err == nil {
				t.Fatal("injected failure was ignored")
			}
			journal, err := groups.LoadJournal(s.SharedRoot(), groups.Fable)
			if err != nil {
				t.Fatal(err)
			}
			if failedRollback {
				if journal == nil {
					t.Fatal("failed rollback lost its journal")
				}
				for _, number := range []string{"1", "2"} {
					owner, err := s.CredentialOwner(number)
					if err != nil || !owner.Uncertain {
						t.Fatalf("uncertain account %s was released: %+v, %v", number, owner, err)
					}
				}
				fable.Creds = original
				if err := withTripleLock(fable, fable.ReconcileGroup); err != nil {
					t.Fatal(err)
				}
			} else if journal != nil {
				t.Fatal("successful rollback left a journal")
			}
			if current := fable.CurrentAccountNumber(); current == nil || *current != "1" {
				t.Fatal("original account was not restored")
			}
			owner, err := s.CredentialOwner("2")
			if err != nil || owner.Scope != "" {
				t.Fatalf("rolled-back target remained owned: %+v, %v", owner, err)
			}
		})
	}
}

func TestGroupCrashReconciliationAndUnknownRefreshHold(t *testing.T) {
	for _, phase := range []string{"claimed", "credential", "config", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			s := groupFixture(t)
			fable := scope(t, s, groups.Fable)
			if _, err := SwitchTo(fable, "1", true, false); err != nil {
				t.Fatal(err)
			}
			creds, _ := s.ReadAccountCredentials("2", "two@x.com")
			err := withTripleLock(fable, func() error {
				_, err := fable.BeginGroupSwitch(groups.Account{Number: "2", Email: "two@x.com"}, creds)
				if err != nil {
					return err
				}
				if phase != "claimed" {
					if err := fable.Creds.WriteActiveAccount(creds); err != nil {
						return err
					}
				}
				if phase == "config" {
					return fable.WriteGroupIdentity(map[string]any{"oauthAccount": map[string]any{"emailAddress": "two@x.com", "organizationUuid": ""}})
				}
				if phase == "unknown" {
					return fable.Creds.WriteActive(groupCreds("max", "new-access", "unknown-lineage"))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := SwitchTo(scope(t, s, groups.Opus), "2", true, false); err == nil {
				t.Fatal("crash claim was stolen")
			}
			err = withTripleLock(fable, fable.ReconcileGroup)
			if phase == "unknown" {
				if err == nil {
					t.Fatal("unknown rotated lineage was released")
				}
				if _, err := os.Stat(groups.JournalPath(s.SharedRoot(), groups.Fable)); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "1"
			if phase == "config" {
				want = "2"
			}
			if got := fable.CurrentAccountNumber(); got == nil || *got != want {
				t.Fatalf("recovery account %v, want %s", got, want)
			}
		})
	}
}

func TestGroupDefaultHandoffAndLiveDefaultRefusal(t *testing.T) {
	s := groupFixture(t)
	opus := scope(t, s, groups.Opus).WithGroupHandoff()
	seqBefore, _ := os.ReadFile(s.SequenceFile)
	defaultConfigBefore, _ := os.ReadFile(s.DefaultConfigPath())
	if _, err := SwitchTo(scope(t, s, groups.Opus), "3", true, false); err == nil {
		t.Fatal("unrequested default handoff succeeded")
	}
	sessions := filepath.Join(s.DefaultProfileDir(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{"pid": os.Getpid()})
	pidFile := filepath.Join(sessions, "self.json")
	if err := os.WriteFile(pidFile, row, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(opus, "3", true, false); err == nil {
		t.Fatal("live default session was moved")
	}
	if err := os.WriteFile(pidFile, []byte(`{"pid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(opus, "3", true, false); err == nil {
		t.Fatal("partial default process registry was treated as no live source")
	}
	if err := os.Remove(pidFile); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(opus, "3", true, false); err != nil {
		t.Fatal(err)
	}
	if s.CurrentAccountNumber() != nil {
		t.Fatal("default identity remained after credential transfer")
	}
	if creds, err := s.ReadDefaultCredentials(); err != nil || creds != "" {
		t.Fatal("default retained a duplicate rotating credential")
	}
	if got, _ := os.ReadFile(s.SequenceFile); string(got) != string(seqBefore) {
		t.Fatal("handoff changed default sequence tracking")
	}
	if got, _ := os.ReadFile(s.DefaultConfigPath()); string(got) == string(defaultConfigBefore) {
		t.Fatal("handoff did not clear default identity")
	}
}

func TestRefreshFinishesBeforeClaimActivation(t *testing.T) {
	s := groupFixture(t)
	fable := scope(t, s, groups.Fable)
	entered, proceed := make(chan struct{}), make(chan struct{})
	rotated := groupCreds("max", "fresh", "rotated")
	c := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
		close(entered)
		<-proceed
		return oauth.RefreshOutcome{Credentials: rotated}
	}}
	before, _ := s.ReadAccountCredentials("1", "one@x.com")
	refreshDone := make(chan oauth.RefreshOutcome, 1)
	go func() { refreshDone <- s.RefreshBackupGuarded(context.Background(), c, "1", "one@x.com", before) }()
	<-entered
	switchDone := make(chan error, 1)
	go func() { _, err := SwitchTo(fable, "1", true, false); switchDone <- err }()
	close(proceed)
	if out := <-refreshDone; out.Credentials != rotated {
		t.Fatalf("refresh failed: %+v", out)
	}
	if err := <-switchDone; err != nil {
		t.Fatal(err)
	}
	if got, _, _ := fable.Creds.ReadActive(); got != rotated {
		t.Fatal("activation copied a stale pre-refresh credential")
	}
}

func TestStoppedLegacyProfileHandsOffWithoutDuplicatingCredential(t *testing.T) {
	s := groupFixture(t)
	dir := s.SessionDir("1", "one@x.com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	value, _ := s.ReadAccountCredentials("1", "one@x.com")
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"one@x.com","organizationUuid":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock.With(func() error { return s.ClaimLegacyProfile("1", "one@x.com", dir) }); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(scope(t, s, groups.Fable), "1", true, false); err == nil {
		t.Fatal("legacy startup reservation was stolen")
	}
	if _, err := SwitchTo(scope(t, s, groups.Fable).WithGroupHandoff(), "1", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatal("legacy profile retained a duplicate credential after handoff")
	}
	if err := s.EnsureSessionAccountAvailable("1", dir); err == nil {
		t.Fatal("legacy bootstrap copied a group-owned credential")
	}
}

func TestExistingStoppedLegacyProfileKeepsItsRefreshedLineage(t *testing.T) {
	s := groupFixture(t)
	s.OAuth = &oauth.FakeClient{ProfileFn: func(_ context.Context, token string) *oauth.Identity {
		if token == "legacy-new-access" {
			return &oauth.Identity{Email: "one@x.com", OrgUUID: ""}
		}
		return nil
	}}
	dir := s.SessionDir("1", "one@x.com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	current := groupCreds("max", "legacy-new-access", "legacy-rotated-refresh")
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"one@x.com","organizationUuid":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fable := scope(t, s, groups.Fable).WithGroupHandoff()
	if _, err := SwitchTo(fable, "1", true, false); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := fable.Creds.ReadActive(); got != current {
		t.Fatal("group took a stale backup instead of the existing profile's refreshed lineage")
	}
	if got, _ := s.ReadAccountCredentials("1", "one@x.com"); got != current {
		t.Fatal("handoff did not preserve the latest lineage in the shared backup")
	}
}
