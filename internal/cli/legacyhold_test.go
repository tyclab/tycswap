package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/oauth"
)

func heldWarnings(t *testing.T, sw *core.Switcher, number string) int {
	t.Helper()
	logged, _ := os.ReadFile(filepath.Join(sw.BackupDir(), "tycswap.log"))
	return strings.Count(string(logged), "Account-"+number+" profile retirement held")
}

// heldFixture gives Account-2 a profile login newer than its stored one that
// the profile endpoint cannot name; expired, the stored login expired earlier.
func heldFixture(t *testing.T, oc *countedOAuth, expired bool) (*core.Switcher, string) {
	t.Helper()
	sw := retireStore(t, oc.client())
	login := retireCredsAt("at-2-new", "rt-2-new", "4102444900000")
	if expired {
		if err := sw.Creds.WriteBackup("2", retireEmails["2"], retireCredsAt("at-2", "rt-2", "1000")); err != nil {
			t.Fatal(err)
		}
		login = retireCredsAt("at-2-new", "rt-2-new", "2000")
	}
	return sw, legacyProfile(t, sw, "2", login, true, false)
}

// RECLAIM-4: a release refused for a login is not asked about again while
// that login stays; the warning is logged once.
func TestHeldLoginNotAskedAgain(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "unexpired", true: "expired"}[expired], func(t *testing.T) {
			oc := &countedOAuth{refresh: func(string) oauth.RefreshOutcome {
				return oauth.RefreshOutcome{Credentials: retireCreds("at-2-refreshed", "rt-2-refreshed")}
			}}
			sw, _ := heldFixture(t, oc, expired)
			scheduledTick(t)
			first := oc.calls()
			if first == 0 || !claimed(t, sw, "2") {
				t.Fatalf("fixture: the first round made %d calls, claimed %v", first, claimed(t, sw, "2"))
			}
			fakeClock(sw).Advance(2 * time.Minute)
			scheduledTick(t)
			if got := oc.calls() - first; got != 0 {
				t.Fatalf("the held login was asked about again: %d calls", got)
			}
			if n := heldWarnings(t, sw, "2"); n != 1 {
				t.Fatalf("held warnings = %d, want 1", n)
			}
		})
	}
}

// A hold names its login: once the profile's login changes, rotation
// eligibility drops the reason and the next round tries the new login.
func TestHoldLastsUntilLoginChanges(t *testing.T) {
	oc := &countedOAuth{}
	sw, dir := heldFixture(t, oc, false)
	scheduledTick(t)
	if reason := sw.RotationOwnershipBlockReason("2"); !strings.Contains(reason, "could not be verified") {
		t.Fatalf("held reason = %q", reason)
	}
	writeTestFile(t, filepath.Join(dir, ".credentials.json"), retireCredsAt("at-2-newer", "rt-2-newer", "4102445000000"), 0o600)
	if reason := sw.RotationOwnershipBlockReason("2"); reason != "" {
		t.Fatalf("a changed login kept the old hold's reason: %q", reason)
	}
	fakeClock(sw).Advance(2 * time.Minute)
	scheduledTick(t)
	if oc.profiles.Load() != 2 {
		t.Fatalf("profile calls = %d, want the changed login asked once", oc.profiles.Load())
	}
	if n := heldWarnings(t, sw, "2"); n != 2 {
		t.Fatalf("held warnings = %d, want one per login", n)
	}
}

// A transport failure is no refusal: it waits out the usage failure
// backoff (30 s after the first failure), then is tried again.
func TestTransientRefreshPausesThenRetries(t *testing.T) {
	refreshed := retireCreds("at-2-refreshed", "rt-2-refreshed")
	oc := &countedOAuth{profile: accountB}
	oc.refresh = func(string) oauth.RefreshOutcome {
		if oc.refreshes.Load() == 1 {
			return oauth.RefreshOutcome{Error: oauth.ErrTransient}
		}
		return oauth.RefreshOutcome{Credentials: refreshed}
	}
	sw, dir := heldFixture(t, oc, true)
	scheduledTick(t)
	if oc.refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d after the first round", oc.refreshes.Load())
	}
	fakeClock(sw).Advance(10 * time.Second)
	scheduledTick(t)
	if oc.refreshes.Load() != 1 {
		t.Fatalf("a round inside the pause refreshed again: %d", oc.refreshes.Load())
	}
	fakeClock(sw).Advance(25 * time.Second)
	scheduledTick(t)
	if oc.refreshes.Load() != 2 {
		t.Fatalf("refreshes = %d, want one retry after the pause", oc.refreshes.Load())
	}
	if got, _ := sw.ReadAccountCredentials("2", retireEmails["2"]); got != refreshed || claimed(t, sw, "2") || exists(filepath.Join(dir, ".credentials.json")) {
		t.Fatalf("the retried release did not finish: stored %q", got)
	}
	if n := heldWarnings(t, sw, "2"); n != 1 {
		t.Fatalf("held warnings = %d, want 1", n)
	}
}
