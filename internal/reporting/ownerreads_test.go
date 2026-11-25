package reporting

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/store/ownertest"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

func ownerStore(t testing.TB, opts ownertest.Options) *ownertest.Fixture {
	t.Helper()
	testutil.IsolateHome(t)
	s, err := store.New(store.Options{Clock: testutil.FixedClock(t, fixedNow), Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	return ownertest.Build(t, s, opts)
}

// One state build, status and list each read every identity file and scan
// every profile's live sessions at most once (DESIGN A116).
func TestReadOperationsReadOwnershipInputsOnce(t *testing.T) {
	for name, op := range map[string]func(s *store.Store){
		"snapshot": func(s *store.Store) { Snapshot(s, nil) },
		"status":   func(s *store.Store) { _, _ = Status(s, true) },
		"list":     func(s *store.Store) { _, _ = ListAccounts(s, false, true, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			f := ownerStore(t, ownertest.Options{Accounts: 6, Fable: true})
			counts := ownertest.CountReads(t)
			op(f.Store)
			counts.AtMostOnce(t, name)
		})
	}
}

// Nothing one state build read answers the next: a session that ended and an
// identity that changed between two snapshots are seen by the second.
func TestSnapshotsDoNotShareOwnershipReads(t *testing.T) {
	f := ownerStore(t, ownertest.Options{Accounts: 6})
	rows := func() map[string]AccountSnapshot {
		out := map[string]AccountSnapshot{}
		for _, a := range Snapshot(f.Store, nil).Accounts {
			out[a.Number] = a
		}
		return out
	}
	before := rows()
	if before["2"].RotationEligible || !strings.Contains(before["2"].OwnershipBlockReason, "running session profile") {
		t.Fatalf("account 2 before its session ended: %+v", before["2"])
	}
	if !before["6"].RotationEligible {
		t.Fatalf("account 6 before account 3's profile signed in as it: %+v", before["6"])
	}
	ownertest.EndSessions(t, f.Legacy["2"])
	if err := os.WriteFile(filepath.Join(f.Legacy["3"], ".claude.json"), []byte(ownertest.Identity(f.Emails["6"], "", 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	after := rows()
	if !after["2"].RotationEligible || after["2"].OwnershipBlockReason != "" {
		t.Errorf("account 2 after its session ended: %+v", after["2"])
	}
	if after["6"].RotationEligible || !strings.Contains(after["6"].OwnershipBlockReason, "Account-6 has a running session profile") {
		t.Errorf("account 6 after account 3's profile signed in as it: %+v", after["6"])
	}
}

// The owner of every account on the fixture with group profiles, former
// session profiles, a recovery journal, a claim that disagrees and live PIDs,
// as recorded from the computation before DESIGN A116; and a snapshot's own
// ownership reasons are the ones asked one by one.
func TestCredentialOwnersAsRecorded(t *testing.T) {
	f := ownerStore(t, ownertest.Options{Accounts: 8, Journal: true, Disagree: true})
	s := f.Store
	pid := []int{os.Getpid()}
	root := s.BackupDir()
	account := func(num string) groups.Account {
		return groups.Account{Number: num, Email: f.Emails[num], OrgUUID: f.Orgs[num]}
	}
	for _, tc := range []struct {
		num       string
		owner     groups.Owner
		ownerErr  string
		available string
	}{
		{num: "1", owner: groups.Owner{Scope: "default", ProfileDir: s.DefaultProfileDir(), Account: account("1"), PIDs: pid}},
		{num: "2", owner: groups.Owner{Scope: "legacy:2", ProfileDir: f.Legacy["2"], Account: account("2"), PIDs: pid}, available: "Account-2 has a running session profile (PID " + strconv.Itoa(pid[0]) + ")"},
		{num: "3", owner: groups.Owner{Scope: "legacy:3", ProfileDir: f.Legacy["3"], Account: account("3"), PIDs: pid}, available: "Account-3 has a running session profile"},
		{num: "4"},
		{num: "5", owner: groups.Owner{Scope: "opus", ProfileDir: f.Group, Account: account("5")}, available: "Account-5 is owned by opus;"},
		{num: "6", owner: groups.Owner{Scope: "fable", ProfileDir: groups.ProfileDir(root, groups.Fable), Account: account("6"), Uncertain: true}, available: "Account-6 is owned by fable (ownership uncertain)"},
		{num: "7"},
		{num: "8", ownerErr: "credential claim disagrees with Account-8's roster identity", available: "credential claim disagrees with Account-8's roster identity"},
	} {
		owner, err := s.CredentialOwner(tc.num)
		if errText(err) != tc.ownerErr || !sameOwner(owner, tc.owner) {
			t.Errorf("CredentialOwner(%s) = %+v, %v; want %+v, %q", tc.num, owner, err, tc.owner, tc.ownerErr)
		}
		if err := s.EnsureAccountAvailable(tc.num); tc.available == "" && err != nil || tc.available != "" && (err == nil || !strings.Contains(err.Error(), tc.available)) {
			t.Errorf("EnsureAccountAvailable(%s) = %v, want %q", tc.num, err, tc.available)
		}
	}
	data, _ := s.ReadSequence()
	reasons := map[string]string{}
	for _, a := range Snapshot(s, nil).Accounts {
		reasons[a.Number] = a.OwnershipBlockReason
		if want := s.RotationOwnershipBlockReason(a.Number); a.OwnershipBlockReason != want {
			t.Errorf("snapshot reason for %s = %q, asked alone %q", a.Number, a.OwnershipBlockReason, want)
		}
		if want := s.RotationEligible(data, a.Number); a.RotationEligible != want {
			t.Errorf("snapshot eligibility for %s = %v, asked alone %v", a.Number, a.RotationEligible, want)
		}
	}
	for num, free := range map[string]bool{"1": true, "2": false, "3": false, "4": true, "5": false, "6": false, "7": true, "8": false} {
		if (reasons[num] == "") != free {
			t.Errorf("account %s ownership block %q, want free=%v", num, reasons[num], free)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func sameOwner(a, b groups.Owner) bool {
	return a.Scope == b.Scope && filepath.Clean(a.ProfileDir) == filepath.Clean(b.ProfileDir) && a.Account == b.Account &&
		a.Uncertain == b.Uncertain && slices.Equal(a.PIDs, b.PIDs)
}

// BenchmarkSnapshotOwnership is one state build's store-only snapshot on 8
// accounts with four 250 KiB identity files (DESIGN A116).
func BenchmarkSnapshotOwnership(b *testing.B) {
	f := ownerStore(b, ownertest.Options{Accounts: 8, PadKiB: 250, Fable: true})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		Snapshot(f.Store, nil)
	}
}
