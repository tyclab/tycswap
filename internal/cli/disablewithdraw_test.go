package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

func withdrawalFixture(t *testing.T) *core.Switcher {
	t.Helper()
	sw, _ := (fableTickFixture{def: "1", live: "1", off: true, logins: []string{"1", "2", "3"},
		usage: map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 30}, "3": {10, 20, 30}, "4": {10, 20, 30}}}).build(t)
	setRecord(t, sw, "2", store.ModelsKey, []string{"opus"})
	setRecord(t, sw, "3", store.ModelsKey, []string{"fable"})
	setRecord(t, sw, "4", "kind", "api_key")
	return sw
}

func TestDisableWithdrawsIdleStoresWithAutomaticSwitchingOff(t *testing.T) {
	sw := withdrawalFixture(t)
	if err := sw.SetAccountDisabled("1", true); err != nil {
		t.Fatal(err)
	}
	if !sw.IsAccountDisabled("1") || !strings.Contains(defaultLive(t, sw), "at-d2") || !strings.Contains(storeLive(t, sw), "at-f3") {
		t.Fatal("idle default and secondary stores did not move to compatible enabled logins")
	}
	if cfg, _, _ := sw.GetCurrentAccount(); cfg != fableEmail("2") {
		t.Fatalf("default config account %q", cfg)
	}
}

func TestDisableParksUnavailableStoresAndPreservesTheirGrants(t *testing.T) {
	sw := withdrawalFixture(t)
	setRecord(t, sw, "2", "disabled", true)
	setRecord(t, sw, "3", "disabled", true)
	f, err := sw.Store.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.SetAccountDisabled("1", true); err != nil {
		t.Fatal(err)
	}
	data, err := sw.ReadSequence()
	if err != nil || data.ActiveAccountNumber != nil || sw.HasLiveLogin() || f.HasLiveLogin() {
		t.Fatalf("unavailable selection: recorded=%v default=%v secondary=%v error=%v", data.ActiveAccountNumber, sw.HasLiveLogin(), f.HasLiveLogin(), err)
	}
	for _, c := range []struct {
		s     *store.Store
		token string
	}{{sw.Store, "at-d1"}, {f, "at-f1"}} {
		backup, err := c.s.ReadAccountCredentials("1", fableEmail("1"))
		if err != nil || !strings.Contains(backup, c.token) {
			t.Fatal("disabled account's saved grant was lost")
		}
	}
	if err := sw.SetAccountDisabled("1", true); err != nil {
		t.Fatalf("repeat withdrawal: %v", err)
	}
	if err := sw.SetAccountDisabled("1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.SwitchTo("1", true); err != nil || !strings.Contains(defaultLive(t, sw), "at-d1") {
		t.Fatalf("saved login could not be selected after re-enable: %v", err)
	}
}

func TestDisablePreservesInUseStoresAndRetries(t *testing.T) {
	for _, tc := range []string{"unknown live session", "default live session", "launch hold", "secondary launch hold", "unreadable registry"} {
		t.Run(tc, func(t *testing.T) {
			sw := withdrawalFixture(t)
			pid := strconv.Itoa(os.Getpid())
			registration := filepath.Join(sw.DefaultProfileDir(), "sessions", pid+".json")
			if strings.HasSuffix(tc, "launch hold") {
				hold := fmt.Sprintf(`{"startHolds":{"test":%d}}`, sw.Clk.Now().Unix()+25)
				if tc == "launch hold" {
					writeFile(t, sw.StatePath(), hold)
				}
				writeFile(t, fableStatePath(sw), hold)
			} else if tc == "unreadable registry" {
				writeFile(t, registration, "{")
			} else {
				writeFile(t, registration, `{"sessionId":"active","pid":`+pid+`}`)
				if tc == "default live session" {
					if _, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), "active", "opus", "", sw.Clk.Now()); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := sw.SetAccountDisabled("1", true)
			if (err != nil) != (tc == "unreadable registry") || !sw.IsAccountDisabled("1") || !strings.Contains(defaultLive(t, sw), "at-d1") {
				t.Fatalf("disabled flag/active process were not preserved: %v", err)
			}
			want := "at-f1"
			if tc == "default live session" {
				want = "at-f3"
			}
			if !strings.Contains(storeLive(t, sw), want) {
				t.Fatalf("wrong secondary selection for %s", tc)
			}
			if strings.HasSuffix(tc, "launch hold") {
				writeFile(t, sw.StatePath(), "{}")
				writeFile(t, fableStatePath(sw), "{}")
			} else if err := os.Remove(registration); err != nil {
				t.Fatal(err)
			}
			if err := sw.SetAccountDisabled("1", true); err != nil || !strings.Contains(defaultLive(t, sw), "at-d2") || !strings.Contains(storeLive(t, sw), "at-f3") {
				t.Fatalf("retry on an already-disabled account did not withdraw the idle selection: %v", err)
			}
		})
	}
}

func TestDisableParksOwnedEndpointAndKeepsSavedKey(t *testing.T) {
	fakeSwitcher(t)
	const address, key = "https://gateway.example.test/anthropic", "sk-gw-synthetic-fixture"
	if code, _, msg := runHuman(t, "add-token", key, "--base-url", address, "--email", "gw@example.test"); code != 0 {
		t.Fatal(msg)
	}
	if code, _, msg := runHuman(t, "switch", "1", "--yes"); code != 0 {
		t.Fatal(msg)
	}
	settingsPath := filepath.Join(paths.GetClaudeConfigHome(), "settings.json")
	if ccsettings.Live(settingsPath).BaseURL != address {
		t.Fatal("native fixture endpoint was not installed")
	}
	if code, _, msg := runHuman(t, "disable", "1"); code != 0 {
		t.Fatal(msg)
	}
	if got := ccsettings.Live(settingsPath); got.BaseURL != "" || got.Token != "" {
		t.Fatal("disabled endpoint remained installed")
	}
	sw, err := newSwitcher(store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	backup, err := sw.ReadAccountCredentials("1", "gw@example.test")
	if err != nil || backup != key || sw.HasLiveLogin() {
		t.Fatalf("parked endpoint grant or active selection incorrect: %v", err)
	}
}

func TestDisableLeavesUnverifiedOAuthOwnershipAlone(t *testing.T) {
	sw := withdrawalFixture(t)
	unknown := dryCreds("unrelated-native-login", "unrelated-native-refresh")
	writeFile(t, filepath.Join(sw.Home, ".claude", ".credentials.json"), unknown)
	before, err := sw.ReadAccountCredentials("1", fableEmail("1"))
	if err != nil {
		t.Fatal(err)
	}
	err = sw.SetAccountDisabled("1", true)
	if err == nil || !strings.Contains(err.Error(), "ownership cannot be verified") || !sw.IsAccountDisabled("1") || defaultLive(t, sw) != unknown {
		t.Fatalf("unverified live owner did not stop default withdrawal: %v", err)
	}
	after, err := sw.ReadAccountCredentials("1", fableEmail("1"))
	if err != nil || before != after {
		t.Fatalf("the disabled account's backup was overwritten: %v", err)
	}
}

type failAfterWithdrawalClear struct{ credstore.Store }

func (s failAfterWithdrawalClear) ClearActive() error {
	if err := s.Store.ClearActive(); err != nil {
		return err
	}
	return errors.New("injected failure after credential clear")
}

func TestDisableParkingRollsBackPartialClearAndCanRetry(t *testing.T) {
	sw := withdrawalFixture(t)
	setRecord(t, sw, "2", "disabled", true)
	setRecord(t, sw, "3", "disabled", true)
	orig := sw.Creds
	before := defaultLive(t, sw)
	sw.Creds = failAfterWithdrawalClear{orig}
	err := sw.SetAccountDisabled("1", true)
	if err == nil || !strings.Contains(err.Error(), "injected failure") || !sw.IsAccountDisabled("1") ||
		defaultLive(t, sw) != before || !sw.HasLiveLogin() {
		t.Fatalf("partial default withdrawal was not rolled back: %v", err)
	}
	sw.Creds = orig
	if err := sw.SetAccountDisabled("1", true); err != nil || sw.HasLiveLogin() {
		t.Fatalf("retry did not complete pending default withdrawal: %v", err)
	}
}

func TestDisableRechecksSourceAfterWaitingForStateLock(t *testing.T) {
	sw, _ := (fableTickFixture{def: "1", off: true}).build(t)
	lock := filelock.New(filepath.Join(sw.BackupDir(), ".autoswitch_state.lock"), time.Second)
	if ok, err := lock.Acquire(0); err != nil || !ok {
		t.Fatalf("acquire fixture lock: %v", err)
	}
	defer lock.Release()
	done := make(chan error, 1)
	go func() { done <- sw.SetAccountDisabled("1", true) }()
	deadline := time.Now().Add(5 * time.Second)
	for !sw.IsAccountDisabled("1") {
		if time.Now().After(deadline) {
			t.Fatal("disable did not release the roster lock before waiting for the state lock")
		}
		time.Sleep(time.Millisecond)
	}
	if err := sw.SetAccountDisabled("1", false); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	if err := <-done; err != nil || sw.IsAccountDisabled("1") || !strings.Contains(defaultLive(t, sw), "at-d1") {
		t.Fatalf("superseded disable changed the re-enabled login: %v", err)
	}
}
