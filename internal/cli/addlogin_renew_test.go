package cli

import (
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/testutil"
)

// renewAs makes the fake claude sign in as email with a new credential.
func renewAs(t *testing.T, email string) {
	t.Helper()
	testutil.Setenv(t, "FAKE_CLAUDE_EMAIL", email)
	testutil.Setenv(t, "FAKE_CLAUDE_TOKEN", "renewed-access")
	testutil.Setenv(t, "FAKE_CLAUDE_REFRESH", "renewed-refresh")
}

// A plain `add --login` as the live account is its repair (A65): the new login
// becomes the live one without --switch.
func TestAddLoginAsTheLiveAccountMakesTheNewLoginLive(t *testing.T) {
	f := newLoginFixture(t)
	renewAs(t, "a@example.com")

	code, out, errb := f.run(t, "add", "--login")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if !strings.Contains(out, "Updated credentials for Account 1 (a@example.com [personal]).") {
		t.Errorf("output = %q", out)
	}
	if live := f.liveCreds(t); !strings.Contains(live, "renewed-access") {
		t.Errorf("live credential = %q, want the new login", live)
	}
	if got := f.storedCreds(t, "1", "a@example.com"); !strings.Contains(got, "renewed-access") {
		t.Errorf("stored credential for slot 1 = %q", got)
	}
	if seq := f.sequence(t); len(seq.Sequence) != 1 || seq.Active == nil || *seq.Active != 1 {
		t.Errorf("sequence = %v active = %v, want one account, active 1", seq.Sequence, seq.Active)
	}
	assertNoScratch(t)
}

func TestAddLoginAsTheLiveAccountEndsNoCredentials(t *testing.T) {
	f := newLoginFixture(t)
	f.writeLive(t, "a@example.com", "", "")
	if _, out, _ := runHuman(t, "list"); !strings.Contains(out, "no credentials") {
		t.Fatalf("list before the login = %q, want the dead live login reported", out)
	}
	renewAs(t, "a@example.com")

	if code, _, errb := f.run(t, "add", "--login"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	code, out, errb := runHuman(t, "list")
	if code != 0 {
		t.Fatalf("list: exit %d, stderr %q", code, errb)
	}
	if strings.Contains(out, "no credentials") {
		t.Errorf("list after the login still reports no credentials: %q", out)
	}
}

func TestAddLoginAsAnotherStoredAccountLeavesTheLiveOneAlone(t *testing.T) {
	f := newLoginFixture(t)
	if code, _, errb := f.run(t, "add", "--login"); code != 0 {
		t.Fatalf("first login: exit %d, stderr %q", code, errb)
	}
	liveBefore := f.liveCreds(t)
	renewAs(t, "b@example.com")

	code, out, errb := f.run(t, "add", "--login")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb)
	}
	if !strings.Contains(out, "Updated credentials for Account 2 (b@example.com [personal]).") {
		t.Errorf("output = %q", out)
	}
	if got := f.storedCreds(t, "2", "b@example.com"); !strings.Contains(got, "renewed-access") {
		t.Errorf("stored credential for slot 2 = %q", got)
	}
	if f.liveCreds(t) != liveBefore {
		t.Errorf("the live credential changed")
	}
	if seq := f.sequence(t); seq.Active == nil || *seq.Active != 1 {
		t.Errorf("activeAccountNumber = %v, want 1", seq.Active)
	}
}
