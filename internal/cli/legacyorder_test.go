package cli

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/usage"
)

// retireCredsAt is a login whose expiresAt is the raw JSON value expiresAt.
func retireCredsAt(access, refresh, expiresAt string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":` + expiresAt + `}}`
}

// countedOAuth counts the token-endpoint and profile-endpoint calls; nil
// functions keep the fake's defaults (a transient refresh, no identity).
type countedOAuth struct {
	refreshes, profiles atomic.Int32
	refresh             func(string) oauth.RefreshOutcome
	profile             func(string) *oauth.Identity
}

func (c *countedOAuth) client() *oauth.FakeClient {
	return &oauth.FakeClient{
		RefreshFn: func(_ context.Context, creds string) oauth.RefreshOutcome {
			c.refreshes.Add(1)
			if c.refresh == nil {
				return oauth.RefreshOutcome{Error: oauth.ErrTransient}
			}
			return c.refresh(creds)
		},
		ProfileFn: func(_ context.Context, token string) *oauth.Identity {
			c.profiles.Add(1)
			if c.profile == nil {
				return nil
			}
			return c.profile(token)
		},
	}
}

func (c *countedOAuth) calls() int32 { return c.refreshes.Load() + c.profiles.Load() }

func accountB(string) *oauth.Identity { return &oauth.Identity{Email: retireEmails["2"]} }

func fakeClock(sw *core.Switcher) *clock.Fake { return sw.Clk.(*clock.Fake) }

// A profile login that expires no later than the slot's stored login of
// another lineage is an older copy: it is dropped without asking the token or
// profile endpoint, and the stored login is never written (DESIGN A92).
func TestOlderProfileLoginDroppedWithoutNetwork(t *testing.T) {
	for name, login := range map[string]string{
		"expired":   retireCredsAt("at-2-old", "rt-2-old", "1"),
		"unexpired": retireCredsAt("at-2-old", "rt-2-old", "4102444700000"),
	} {
		t.Run(name, func(t *testing.T) {
			oc := &countedOAuth{profile: accountB, refresh: func(string) oauth.RefreshOutcome {
				return oauth.RefreshOutcome{Credentials: retireCreds("at-2-refreshed", "rt-2-refreshed")}
			}}
			sw := retireStore(t, oc.client())
			dir := legacyProfile(t, sw, "2", login, true, false)
			before := credentialFiles(t, sw)
			scheduledTick(t)
			if oc.refreshes.Load() != 0 || oc.profiles.Load() != 0 {
				t.Fatalf("an older profile login went out: %d refreshes, %d profile calls", oc.refreshes.Load(), oc.profiles.Load())
			}
			sameFiles(t, before, credentialFiles(t, sw))
			if claimed(t, sw, "2") || exists(filepath.Join(dir, ".credentials.json")) {
				t.Fatal("the older profile login or its claim survived")
			}
		})
	}
}

// Guard: a profile login that expires later than the stored one still takes
// the verified release and is saved.
func TestNewerProfileLoginReleased(t *testing.T) {
	oc := &countedOAuth{profile: accountB}
	sw := retireStore(t, oc.client())
	login := retireCredsAt("at-2-new", "rt-2-new", "4102444900000")
	dir := legacyProfile(t, sw, "2", login, true, false)
	scheduledTick(t)
	if got, _ := sw.ReadAccountCredentials("2", retireEmails["2"]); got != login {
		t.Fatalf("the newer profile login was not saved: %q", got)
	}
	if claimed(t, sw, "2") || exists(filepath.Join(dir, ".credentials.json")) {
		t.Fatal("the released profile kept its login or claim")
	}
}

// A stored login known dead orders nothing: the expired profile login takes
// the refresh path.
func TestDeadBackupTakesRefreshPath(t *testing.T) {
	refreshed := retireCreds("at-2-refreshed", "rt-2-refreshed")
	oc := &countedOAuth{profile: accountB, refresh: func(string) oauth.RefreshOutcome { return oauth.RefreshOutcome{Credentials: refreshed} }}
	sw := retireStore(t, oc.client())
	ids := map[string]usage.Identity{"2": {Email: retireEmails["2"]}}
	if err := sw.Usage.Record(map[string]usage.FetchRecord{"2": {Error: oauth.ErrInvalidGrant}}, ids); err != nil {
		t.Fatal(err)
	}
	if !sw.Usage.Entries(ids)["2"].TokenDead() {
		t.Fatal("fixture: the stored login is not known dead")
	}
	legacyProfile(t, sw, "2", retireCredsAt("at-2-old", "rt-2-old", "1"), true, false)
	scheduledTick(t)
	if oc.refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want the refresh path", oc.refreshes.Load())
	}
	if got, _ := sw.ReadAccountCredentials("2", retireEmails["2"]); got != refreshed {
		t.Fatalf("the refreshed profile login was not saved: %q", got)
	}
}

// Either expiry unreadable: nothing goes out and the login and claim stay held.
func TestUnorderedProfileLoginHeld(t *testing.T) {
	for name, logins := range map[string][2]string{
		"profile": {retireCredsAt("at-2-x", "rt-2-x", `"soon"`), retireCreds("at-2", "rt-2")},
		"stored":  {retireCredsAt("at-2-x", "rt-2-x", "1"), retireCredsAt("at-2", "rt-2", "null")},
	} {
		t.Run(name, func(t *testing.T) {
			oc := &countedOAuth{profile: accountB}
			sw := retireStore(t, oc.client())
			if err := sw.Creds.WriteBackup("2", retireEmails["2"], logins[1]); err != nil {
				t.Fatal(err)
			}
			dir := legacyProfile(t, sw, "2", logins[0], true, false)
			before := credentialFiles(t, sw)
			scheduledTick(t)
			if oc.calls() != 0 {
				t.Fatalf("an unordered login went out: %d refreshes, %d profile calls", oc.refreshes.Load(), oc.profiles.Load())
			}
			sameFiles(t, before, credentialFiles(t, sw))
			if got, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); !claimed(t, sw, "2") || string(got) != logins[0] {
				t.Fatal("an unordered profile login lost its login or claim")
			}
			if reason := sw.RotationOwnershipBlockReason("2"); reason == "" {
				t.Fatal("the unordered login is not held")
			}
		})
	}
}

// The expiry order is taken again under the locks, before the save: a slot
// login that moves past the profile's while the profile endpoint is asked
// is kept, and the profile's is not saved over it.
func TestBackupRewrittenBeforeSaveIsKept(t *testing.T) {
	newer := retireCredsAt("at-2-newest", "rt-2-newest", "4102445000000")
	var sw *core.Switcher
	oc := &countedOAuth{profile: func(string) *oauth.Identity {
		if err := sw.Creds.WriteBackup("2", retireEmails["2"], newer); err != nil {
			t.Error(err)
		}
		return accountB("")
	}}
	sw = retireStore(t, oc.client())
	legacyProfile(t, sw, "2", retireCredsAt("at-2-new", "rt-2-new", "4102444900000"), true, false)
	scheduledTick(t)
	if got, _ := sw.ReadAccountCredentials("2", retireEmails["2"]); got != newer || oc.refreshes.Load() != 0 {
		t.Fatalf("stored login %q after %d refreshes; want the newer one kept", got, oc.refreshes.Load())
	}
}
