package store_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/store/ownertest"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

func viewFixture(t *testing.T) *ownertest.Fixture {
	t.Helper()
	testutil.IsolateHome(t)
	s, err := store.New(store.Options{Clock: testutil.FixedClock(t, "2026-11-25T09:00:00Z"), Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	return ownertest.Build(t, s, ownertest.Options{Accounts: 8, Journal: true, Disagree: true, Fable: true})
}

type ownerAnswers struct {
	Owner       any
	OwnerErr    string
	Available   string
	BlockReason string
	Eligible    bool
	Release     any
	ReleaseErr  string
	Owned       string
	OwnedErr    string
}

func answersOf(s *store.Store, num, email string) ownerAnswers {
	data, _ := s.ReadSequence()
	var a ownerAnswers
	owner, err := s.CredentialOwner(num)
	a.Owner, a.OwnerErr = owner, text(err)
	a.Available = text(s.EnsureAccountAvailable(num))
	a.BlockReason = s.RotationOwnershipBlockReason(num)
	a.Eligible = s.RotationEligible(data, num)
	release, err := s.LegacyReleaseCandidate(num)
	a.Release, a.ReleaseErr = release, text(err)
	a.Owned, err = s.ReadOwnedCredentials(num, email)
	a.OwnedErr = text(err)
	return a
}

func text(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// One read operation answers every ownership question as reading afresh does
// (DESIGN A116), and reads each identity file and profile once doing so.
func TestOneReadAnswersAsFreshReads(t *testing.T) {
	f := viewFixture(t)
	s := f.Store
	view := s.OneRead()
	counts := ownertest.CountReads(t)
	for num, email := range f.Emails {
		got, want := answersOf(view, num, email), answersOf(s, num, email)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("account %s in one read: %+v\nread afresh: %+v", num, got, want)
		}
	}
	if got, want := view.SwitchableAccountNumbers(), s.SwitchableAccountNumbers(); !reflect.DeepEqual(got, want) {
		t.Errorf("SwitchableAccountNumbers in one read %v, afresh %v", got, want)
	}
	if got, want := view.RotationBlockReasons(), s.RotationBlockReasons(); !reflect.DeepEqual(got, want) {
		t.Errorf("RotationBlockReasons in one read %v, afresh %v", got, want)
	}
	counts.Reset()
	for num, email := range f.Emails {
		answersOf(s.OneRead(), num, email)
	}
	if identity, _ := counts.Most(); identity <= 1 {
		t.Errorf("separate read operations shared their reads:\n%s", counts)
	}
	counts.Reset()
	view = s.OneRead()
	for num := range f.Emails {
		_ = view.RotationOwnershipBlockReason(num)
		_, _ = view.CredentialOwner(num)
	}
	counts.AtMostOnce(t, "one read over every account")
}

// What one read operation read never decides a write: on a store from
// OneRead every guard reads afresh.
func TestWriteGuardsReadAfreshInOneRead(t *testing.T) {
	f := viewFixture(t)
	s := f.Store
	view := s.OneRead()
	for num := range f.Emails {
		_, _ = view.CredentialOwner(num)
		_ = view.RotationOwnershipBlockReason(num)
	}
	// Account 2's profile, still running its session, signs in as account 4.
	if err := os.WriteFile(filepath.Join(f.Legacy["2"], ".claude.json"), []byte(ownertest.Identity(f.Emails["4"], "", 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := view.EnsureAccountAvailable("4"); err == nil || !strings.Contains(err.Error(), "Account-4 has a running session profile") {
		t.Errorf("EnsureAccountAvailable(4) = %v, want the running profile that signed in as it", err)
	}
	if _, err := view.LegacyReleaseCandidate("4"); err == nil || !strings.Contains(err.Error(), "running session profile") {
		t.Errorf("LegacyReleaseCandidate(4) = %v, want the running profile", err)
	}
	refreshed := false
	client := &oauth.FakeClient{RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
		refreshed = true
		return oauth.RefreshOutcome{Credentials: ownertest.Creds("4-new")}
	}}
	if out := view.RefreshBackupGuarded(context.Background(), client, "4", f.Emails["4"], ownertest.Creds("4")); refreshed || out.Error != store.RefreshDeclined {
		t.Errorf("RefreshBackupGuarded(4) = %+v, refreshed %v; want declined for the new owner", out, refreshed)
	}
	if err := view.PersistBackupCredentials("4", f.Emails["4"], ownertest.Creds("4-new")); err == nil {
		t.Error("PersistBackupCredentials(4) wrote over a login a running profile holds")
	}
	if backup, _ := s.ReadAccountCredentials("4", f.Emails["4"]); backup != ownertest.Creds("4") {
		t.Errorf("account 4's backup changed: %s", backup)
	}

	// Account 3's session ends: its idle profile is a release candidate now.
	ownertest.EndSessions(t, f.Legacy["3"])
	if candidate, err := view.LegacyReleaseCandidate("3"); err != nil || candidate == nil {
		t.Errorf("LegacyReleaseCandidate(3) = %+v, %v; want the idle profile", candidate, err)
	}
}
