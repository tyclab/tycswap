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
			if err := sw.Lock.With(func() error { return sw.ClaimLegacyProfile("2", "b@example.test", dir) }); err != nil {
				t.Fatal(err)
			}
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
