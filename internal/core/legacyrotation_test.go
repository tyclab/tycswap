package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/settings"
)

func TestAutomaticRotationFromLegacyProfile(t *testing.T) {
	for _, mode := range []string{"idle", "live", "disabled", "dry-run", "rotated", "expired", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			sw := newTestSwitcher(t)
			a, b := oauthCreds("tok-a"), oauthCreds("tok-b")
			if mode == "expired" {
				b = strings.ReplaceAll(b, "4102444800000", "1")
			}
			seedManaged(t, sw, "1", "a@example.test", a)
			seedManaged(t, sw, "2", "b@example.test", b)
			seedManaged(t, sw, "3", "c@example.test", oauthCreds("tok-c"))
			writeSeqDirect(t, sw, ptrInt(1), []int{1, 2, 3}, map[string]json.RawMessage{
				"1": rawRecord(map[string]any{"email": "a@example.test"}),
				"2": rawRecord(map[string]any{"email": "b@example.test", "disabled": mode == "disabled"}),
				"3": rawRecord(map[string]any{"email": "c@example.test"}),
			})
			seedLive(t, sw, "a@example.test", a)
			dir := sw.SessionDir("2", "b@example.test")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			latest := b
			if mode == "rotated" || mode == "unverified" {
				latest = oauthCreds("rotated-b")
			}
			for name, value := range map[string]string{".claude.json": backupConfig("b@example.test"), ".credentials.json": latest, "history.jsonl": "keep this conversation\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			claimLegacy(t, sw, "2", dir)
			if mode == "live" {
				if err := os.Mkdir(filepath.Join(dir, "sessions"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "sessions", "live.json"), []byte(`{"pid":`+strconv.Itoa(os.Getpid())+`}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fc := &oauth.FakeClient{
				UsageFn: func(_ context.Context, token string) (map[string]any, error) {
					pct := 100.0
					if token == "tok-b" || token == "rotated-b" || token == "refreshed-b" {
						pct = 10
					}
					return map[string]any{"five_hour": map[string]any{"utilization": pct}, "seven_day": map[string]any{"utilization": 10.0}}, nil
				},
				ProfileFn: func(context.Context, string) *oauth.Identity {
					if mode == "unverified" {
						return nil
					}
					return &oauth.Identity{Email: "b@example.test"}
				},
				RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
					if mode != "expired" {
						return oauth.RefreshOutcome{Error: oauth.ErrTransient}
					}
					return oauth.RefreshOutcome{Credentials: oauthCreds("refreshed-b")}
				},
			}
			sw.OAuth = fc
			for _, account := range sw.AccountsSnapshot(map[string]bool{}).Accounts {
				if account.Number != "2" {
					continue
				}
				if mode == "live" && (account.RotationEligible || account.OwnershipBlockReason == "") {
					t.Fatal("dashboard advertised an account held by a running profile")
				}
				if mode == "idle" && !account.RotationEligible {
					t.Fatal("dashboard hid a reclaimable profile")
				}
			}
			var events []autoswitch.Event
			e := autoswitch.NewEngine(autoswitchAdapter{sw}, settings.Default(), func(ev autoswitch.Event) { events = append(events, ev) }, mode == "dry-run",
				autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(fc))
			got := e.Tick()
			wantSwitch := mode == "idle" || mode == "rotated" || mode == "expired"
			if wantSwitch {
				if got != autoswitch.Switched || *sw.CurrentAccountNumber() != "2" {
					t.Fatalf("unused profile did not return to rotation: outcome %v, events %v", got, events)
				}
				if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); !os.IsNotExist(err) {
					t.Fatal("released profile kept a duplicate login")
				}
				live, err := sw.ReadDefaultCredentials()
				wantLive := latest
				if mode == "expired" {
					wantLive = oauthCreds("refreshed-b")
				}
				if err != nil || live != wantLive {
					t.Fatal("rotation did not use the latest profile login")
				}
			} else {
				if mode == "dry-run" && got != autoswitch.Switched {
					t.Fatalf("dry-run did not propose reclaiming the unused profile: %v", events)
				}
				if *sw.CurrentAccountNumber() != "1" {
					t.Fatal("held or dry-run account was activated")
				}
				value, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
				if err != nil || string(value) != latest {
					t.Fatal("held or dry-run profile changed")
				}
				if mode == "live" {
					found := false
					for _, ev := range events {
						if ev.Kind() == "all-exhausted" {
							t.Fatal("account with quota reported as exhausted instead of owned")
						}
						if ev, ok := ev.(autoswitch.NoSwitchEvent); ok && ev.Reason == "accounts-in-use" && strings.Contains(ev.Detail, "Account-2") {
							found = true
						}
					}
					if !found {
						t.Fatalf("missing ownership explanation: %v", events)
					}
				}
			}
			if value, err := os.ReadFile(filepath.Join(dir, "history.jsonl")); err != nil || string(value) != "keep this conversation\n" {
				t.Fatal("conversation history changed")
			}
		})
	}
}

// claimLegacy records the claim `tycswap run` wrote for a profile; nothing
// writes one since its removal (DESIGN A92), but stores still hold them.
func claimLegacy(t *testing.T, sw *Switcher, number, dir string) {
	t.Helper()
	id := sw.AccountIdentity(number)
	registry, err := groups.LoadRegistry(sw.SharedRoot())
	if err != nil {
		t.Fatal(err)
	}
	registry.Claims[number] = groups.Owner{Scope: "legacy:" + number, ProfileDir: dir,
		Account: groups.Account{Number: number, Email: id["email"], OrgUUID: id["organizationUuid"], UUID: id["uuid"]}}
	if err := registry.Save(sw.SharedRoot()); err != nil {
		t.Fatal(err)
	}
}

// legacyReview seeds 1 (live, at its 5h limit) and 2, and gives 2 a claimed
// former profile holding login.
func legacyReview(t *testing.T, login string) (*Switcher, string) {
	t.Helper()
	sw := newTestSwitcher(t)
	seedManaged(t, sw, "1", "a@example.test", oauthCreds("tok-a"))
	seedManaged(t, sw, "2", "b@example.test", oauthCreds("tok-b"))
	writeSeqDirect(t, sw, ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": rawRecord(map[string]any{"email": "a@example.test"}),
		"2": rawRecord(map[string]any{"email": "b@example.test"}),
	})
	seedLive(t, sw, "a@example.test", oauthCreds("tok-a"))
	dir := sw.SessionDir("2", "b@example.test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{".claude.json": backupConfig("b@example.test"), ".credentials.json": login} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	claimLegacy(t, sw, "2", dir)
	return sw, dir
}

func reviewUsage(activePct float64) func(context.Context, string) (map[string]any, error) {
	return func(_ context.Context, token string) (map[string]any, error) {
		pct := 10.0
		if token == "tok-a" {
			pct = activePct
		}
		return map[string]any{"five_hour": map[string]any{"utilization": pct}, "seven_day": map[string]any{"utilization": 10.0}}, nil
	}
}

// RECLAIM-3: a login that rotated in its profile and then expired cannot name
// its account to the profile endpoint; its own refresh verifies it, and a
// refused refresh drops the dead login and keeps the stored one.
func TestRotatedExpiredProfileVerifiedByRefresh(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(strconv.FormatBool(rejected), func(t *testing.T) {
			rotated := strings.ReplaceAll(oauthCreds("rotated-b"), "4102444800000", "1")
			sw, dir := legacyReview(t, rotated)
			refreshed := oauthCreds("refreshed-b")
			fc := &oauth.FakeClient{
				UsageFn: reviewUsage(100),
				ProfileFn: func(_ context.Context, token string) *oauth.Identity {
					if token == "rotated-b" {
						return nil
					}
					return &oauth.Identity{Email: "b@example.test"}
				},
				RefreshFn: func(_ context.Context, creds string) oauth.RefreshOutcome {
					if creds != rotated {
						t.Errorf("refreshed another login than the profile's")
					}
					if rejected {
						return oauth.RefreshOutcome{Error: oauth.ErrInvalidGrant}
					}
					return oauth.RefreshOutcome{Credentials: refreshed}
				},
			}
			sw.OAuth = fc
			e := autoswitch.NewEngine(autoswitchAdapter{sw}, settings.Default(), func(autoswitch.Event) {}, false,
				autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(fc))
			got := e.Tick()
			if owner, err := sw.CredentialOwner("2"); err != nil || strings.HasPrefix(owner.Scope, "legacy:") {
				t.Fatalf("the profile still owns Account-2: %+v, %v", owner, err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); !os.IsNotExist(err) {
				t.Fatal("the profile kept a login")
			}
			want := refreshed
			if rejected {
				want = oauthCreds("tok-b")
			}
			if backup, _ := sw.ReadAccountCredentials("2", "b@example.test"); backup != want {
				t.Fatalf("Account-2's stored login = %s, want %s", backup, want)
			}
			if live, _ := sw.ReadDefaultCredentials(); got != autoswitch.Switched || live != want {
				t.Fatalf("outcome %v, live login %s; want a switch to %s", got, live, want)
			}
		})
	}
}

// A refreshed login the profile endpoint cannot name yet stays in the profile,
// where the next round verifies it; the stored login is not touched.
func TestRefreshedProfileLoginKeptUntilVerified(t *testing.T) {
	rotated := strings.ReplaceAll(oauthCreds("rotated-b"), "4102444800000", "1")
	sw, dir := legacyReview(t, rotated)
	refreshed := oauthCreds("refreshed-b")
	fc := &oauth.FakeClient{
		UsageFn: reviewUsage(100),
		RefreshFn: func(context.Context, string) oauth.RefreshOutcome {
			return oauth.RefreshOutcome{Credentials: refreshed}
		},
	}
	sw.OAuth = fc
	e := autoswitch.NewEngine(autoswitchAdapter{sw}, settings.Default(), func(autoswitch.Event) {}, false,
		autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(fc))
	if got := e.Tick(); got == autoswitch.Switched {
		t.Fatal("switched to an unverified account")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); string(got) != refreshed {
		t.Fatalf("the refreshed login is not in the profile: %s", got)
	}
	if backup, _ := sw.ReadAccountCredentials("2", "b@example.test"); backup != oauthCreds("tok-b") {
		t.Fatalf("the stored login changed: %s", backup)
	}
	if owner, err := sw.CredentialOwner("2"); err != nil || owner.Scope != "legacy:2" {
		t.Fatalf("the unverified profile lost its claim: %+v, %v", owner, err)
	}
}

// RECLAIM-4: once a release cannot verify the profile's login, the snapshot
// says why instead of offering the account the engine refuses.
func TestHeldReleaseShowsReason(t *testing.T) {
	sw, _ := legacyReview(t, oauthCreds("rotated-b"))
	fc := &oauth.FakeClient{UsageFn: reviewUsage(100)}
	sw.OAuth = fc
	e := autoswitch.NewEngine(autoswitchAdapter{sw}, settings.Default(), func(autoswitch.Event) {}, false,
		autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(fc))
	if got := e.Tick(); got == autoswitch.Switched {
		t.Fatal("switched to an account whose login could not be verified")
	}
	for _, a := range sw.AccountsSnapshot(map[string]bool{}).Accounts {
		if a.Number == "2" && (a.RotationEligible || !strings.Contains(a.OwnershipBlockReason, "could not be verified")) {
			t.Fatalf("Account-2 eligible %v, reason %q; want held with the reason", a.RotationEligible, a.OwnershipBlockReason)
		}
	}
}

// RECLAIM-5: a reserved hand-over with nowhere to go is one line, not two.
func TestHandoverWithHeldAccountEmitsOneLine(t *testing.T) {
	sw, dir := legacyReview(t, oauthCreds("tok-b"))
	if err := os.Mkdir(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", "live.json"), []byte(`{"pid":`+strconv.Itoa(os.Getpid())+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fc := &oauth.FakeClient{UsageFn: reviewUsage(10)}
	sw.OAuth = fc
	var lines []string
	e := autoswitch.NewEngine(autoswitchAdapter{sw}, settings.Default(), func(ev autoswitch.Event) {
		if ns, ok := ev.(autoswitch.NoSwitchEvent); ok {
			lines = append(lines, ns.Reason)
		}
	}, false, autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(fc), autoswitch.WithReserved(func(num string) bool { return num == "1" }))
	e.Tick()
	if len(lines) != 1 || lines[0] != "reserved-no-target" {
		t.Fatalf("no-switch lines %v, want one reserved-no-target", lines)
	}
}
