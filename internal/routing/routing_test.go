package routing

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/wincred"
)

func quota(pct float64) map[string]any {
	return map[string]any{"five_hour": map[string]any{"pct": pct}, "seven_day": map[string]any{"pct": pct}, "scoped": []any{map[string]any{"name": "Fable", "pct": pct}, map[string]any{"name": "Opus", "pct": pct}}}
}

func fixture(t *testing.T) (Coordinator, Request) {
	t.Helper()
	testutil.IsolateHome(t)
	s, err := store.New(store.Options{Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetupDirectories(); err != nil {
		t.Fatal(err)
	}
	data := &store.SequenceData{Sequence: []int{1, 2, 3}, Accounts: map[string]json.RawMessage{}}
	for _, number := range data.Sequence {
		num := fmt.Sprint(number)
		email := "route" + num + "@example.invalid"
		plan := "max"
		if num == "3" {
			plan = "pro"
		}
		data.Accounts[num], _ = json.Marshal(map[string]any{"email": email, "organizationUuid": "", "subscriptionType": plan})
		creds, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "test-route-" + num, "refreshToken": "test-refresh-" + num, "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": plan, "scopes": []string{"user:inference", "user:profile"}}})
		config, _ := json.Marshal(map[string]any{"oauthAccount": map[string]any{"emailAddress": email, "organizationUuid": ""}})
		if err := s.WriteAccountCredentials(num, email, string(creds)); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteAccountConfig(num, email, string(config)); err != nil {
			t.Fatal(err)
		}
		pct := map[string]float64{"1": 90, "2": 20, "3": 10}[num]
		if err := s.Usage.Record(map[string]usage.FetchRecord{num: {Usage: quota(pct)}}, map[string]usage.Identity{num: {Email: email}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
	s, err = s.ForGroup(groups.Fable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := switching.SwitchTo(s, "2", true, false); err != nil {
		t.Fatal(err)
	}
	r := Request{Session: "session", Launch: "launch", PID: os.Getpid(), Model: "fable", Main: "fable"}
	if err := routingstate.With(s.BackupDir(), func(state *routingstate.State) error {
		state.Sessions["fable/launch"] = routingstate.Session{ID: "launch", Group: "fable", Model: "fable", Launch: r.Launch, PID: r.PID}
		state.Leases["fable/startup-launch"] = routingstate.Lease{ID: "startup-launch", Group: "fable", Session: "launch", Account: "2", Model: "fable", PID: r.PID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c := Coordinator{Store: s}
	if _, err := c.Register(r); err != nil {
		t.Fatal(err)
	}
	return c, r
}

func TestRuntimeModelRoutingAndChildren(t *testing.T) {
	c, request := fixture(t)
	for index, model := range []string{"fable", "opus", "fable[1m]"} {
		request.ID, request.Main, request.Model = fmt.Sprint(index), model, model
		want := "2"
		if model == "opus" {
			want = "3"
		}
		lease, err := c.Acquire(request)
		if err != nil || lease.Account != want {
			t.Fatalf("%s acquire = %#v, %v; want %s", model, lease, err, want)
		}
		child := request
		child.ID, child.Agent, child.Model = "child", "worker", "claude-opus-4-6"
		got, err := c.Acquire(child)
		if err != nil || got.Account != want {
			t.Fatalf("child = %#v, %v", got, err)
		}
		if _, err := c.Release(child); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Release(request); err != nil {
			t.Fatal(err)
		}
	}
	request.ID, request.Main, request.Model, request.Agent = "denied", "opus", "fable", "worker"
	if _, err := c.Acquire(request); err == nil || !strings.Contains(err.Error(), "Fable child") {
		t.Fatalf("Fable child under Opus accepted: %v", err)
	}
}

func TestHeldRequestsBlockModelChangeButAllowSameAccount(t *testing.T) {
	c, first := fixture(t)
	first.ID = "first"
	if _, err := c.Acquire(first); err != nil {
		t.Fatal(err)
	}
	worker := first
	worker.Model = "claude-fable-1-1"
	if _, err := c.Register(worker); err != nil {
		t.Fatalf("worker could not reuse active registration: %v", err)
	}
	second := first
	second.ID = "second"
	if _, err := c.Acquire(second); err != nil {
		t.Fatal(err)
	}
	change := first
	change.ID, change.Main, change.Model = "changed", "opus", "opus"
	if _, err := c.Acquire(change); err == nil {
		t.Fatal("model changed with requests pending")
	}
	if _, err := c.Unregister(first); err == nil {
		t.Fatal("session with pending requests unregistered")
	}
	for _, request := range []Request{first, second} {
		if _, err := c.Release(request); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := c.Acquire(change); err != nil || result.Account != "3" {
		t.Fatalf("change after release = %#v, %v", result, err)
	}
}

func TestRegistrationBindsLaunchAndSurvivesSessionClear(t *testing.T) {
	c, request := fixture(t)
	foreign := request
	foreign.Launch = "unreserved"
	if _, err := c.Register(foreign); err == nil {
		t.Fatal("foreign launch replaced session")
	}
	if _, err := c.Unregister(request); err != nil {
		t.Fatal(err)
	}
	request.Session = "cleared-session"
	if _, err := c.Register(request); err != nil {
		t.Fatal(err)
	}
	request.ID = "request"
	if _, err := c.Acquire(request); err != nil {
		t.Fatal(err)
	}
	foreign = request
	foreign.Launch = "foreign"
	if _, err := c.Release(foreign); err == nil {
		t.Fatal("foreign launch released active request")
	}
}

func TestQuotaRejectsUnknownInvalidAndExhaustedWindows(t *testing.T) {
	now := float64(time.Now().Unix())
	for _, tc := range []struct {
		name string
		edit func(*usage.UsageEntry)
	}{
		{"missing-shared", func(e *usage.UsageEntry) { delete(e.LastGood, "seven_day") }},
		{"missing-fable", func(e *usage.UsageEntry) { delete(e.LastGood, "scoped") }},
		{"stale", func(e *usage.UsageEntry) { old := now - 181; e.FetchedAt = &old }},
		{"sentinel", func(e *usage.UsageEntry) { e.Sentinel = "expired" }},
		{"invalid", func(e *usage.UsageEntry) { e.LastGood["five_hour"] = map[string]any{"pct": math.NaN()} }},
		{"model-limit", func(e *usage.UsageEntry) { e.LastGood["scoped"] = []any{map[string]any{"name": "Fable", "pct": 95.0}} }},
		{"weekly-limit", func(e *usage.UsageEntry) { e.LastGood["seven_day"] = map[string]any{"pct": 97.0} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := usage.UsageEntry{FetchedAt: &now, LastGood: quota(20)}
			tc.edit(&entry)
			if _, ok := headroom(entry, now, map[string]bool{"fable": true}, settings.Default()); ok {
				t.Fatal("unsafe quota accepted")
			}
		})
	}
}

func TestSelectionPrefersOrdinaryAccountAndHysteresis(t *testing.T) {
	if got := choose([]candidate{{"max", 1, 90}, {"pro", 0, 40}}, "max", 10); got != "pro" {
		t.Fatalf("preferred %s instead of pro", got)
	}
	if got := choose([]candidate{{"active", 0, 50}, {"other", 0, 55}}, "active", 10); got != "active" {
		t.Fatal("small usage difference churned accounts")
	}
}

func TestIdleProfileTransferAndLiveOwnerProtection(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint(live), func(t *testing.T) {
			c, request := fixture(t)
			other, err := c.Store.ForGroup(groups.Opus)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := switching.SwitchTo(other, "3", true, false); err != nil {
				t.Fatal(err)
			}
			if live {
				if err := routingstate.With(c.Store.BackupDir(), func(state *routingstate.State) error {
					state.Sessions["opus/other"] = routingstate.Session{ID: "other", Group: "opus", PID: os.Getpid(), Model: "opus", Launch: "other-launch"}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			request.ID, request.Main, request.Model = "request", "opus", "opus"
			result, err := c.Acquire(request)
			want := "3"
			if live {
				want = "2"
			}
			if err != nil || result.Account != want {
				t.Fatalf("acquire = %#v, %v, want %s", result, err, want)
			}
			active, err := groups.LoadActive(c.Store.BackupDir(), groups.Opus)
			if err != nil || (active != nil) != live {
				t.Fatalf("source profile active = %#v, %v", active, err)
			}
		})
	}
}

func TestMissingQuotaDoesNotSwitchOrLease(t *testing.T) {
	c, request := fixture(t)
	if err := os.Remove(filepath.Join(c.Store.BackupDir(), "cache", "usage.json")); err != nil {
		t.Fatal(err)
	}
	request.ID, request.Main, request.Model = "request", "opus", "opus"
	if _, err := c.Acquire(request); err == nil {
		t.Fatal("unknown quota routed")
	}
	if current := c.Store.CurrentAccountNumber(); current == nil || *current != "2" {
		t.Fatal("refusal changed active credentials")
	}
	state, err := routingstate.Read(c.Store.BackupDir())
	if err != nil || len(state.Leases) != 0 || state.Sessions["fable/session"].Model != "fable" {
		t.Fatalf("refusal changed routing state: %#v, %v", state, err)
	}
}

func TestNativeFallbackUsesActualQuota(t *testing.T) {
	c, request := fixture(t)
	request.ID, request.Main, request.Model = "fallback", "fable", "claude-haiku-4-5"
	if result, err := c.Acquire(request); err != nil || result.Account != "2" {
		t.Fatalf("native fallback = %#v, %v", result, err)
	}
	now := float64(time.Now().Unix())
	entry := usage.UsageEntry{FetchedAt: &now, LastGood: quota(10)}
	entry.LastGood["scoped"] = []any{map[string]any{"name": "Opus", "pct": "unknown"}}
	if _, ok := headroom(entry, now, map[string]bool{"opus": true}, settings.Default()); ok {
		t.Fatal("invalid reported model quota was treated as absent")
	}
}

func TestParallelProfilesKeepSeparateLeasedAccounts(t *testing.T) {
	fable, first := fixture(t)
	opusStore, err := fable.Store.ForGroup(groups.Opus)
	if err != nil {
		t.Fatal(err)
	}
	opus := Coordinator{Store: opusStore}
	second := Request{ID: "second", Session: "other", Launch: "other-launch", PID: os.Getpid(), Main: "opus", Model: "opus"}
	if err := routingstate.With(fable.Store.BackupDir(), func(state *routingstate.State) error {
		state.Sessions["opus/other"] = routingstate.Session{Group: "opus", ID: second.Session, PID: second.PID, Model: "opus", Launch: second.Launch}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first.ID = "first"
	type outcome struct {
		result Result
		err    error
	}
	left, right := make(chan outcome, 1), make(chan outcome, 1)
	go func() { result, err := fable.Acquire(first); left <- outcome{result, err} }()
	go func() { result, err := opus.Acquire(second); right <- outcome{result, err} }()
	for _, check := range []struct {
		channel chan outcome
		want    string
	}{{left, "2"}, {right, "3"}} {
		got := <-check.channel
		if got.err != nil || got.result.Account != check.want {
			t.Fatalf("parallel acquire = %#v, %v, want %s", got.result, got.err, check.want)
		}
	}
	if *fable.Store.CurrentAccountNumber() != "2" || *opus.Store.CurrentAccountNumber() != "3" {
		t.Fatal("parallel profiles changed one another's credentials")
	}
}

func TestUnmanagedProfileProcessesPreventRouting(t *testing.T) {
	for _, tc := range []struct {
		name, directory, file, body string
		allowed                     bool
	}{
		{"registered", "sessions", "native.json", fmt.Sprintf(`{"pid":%d,"sessionId":"session"}`, os.Getpid()), true},
		{"legacy", "sessions", "legacy.json", fmt.Sprintf(`{"pid":%d,"sessionId":"legacy"}`, os.Getpid()), false},
		{"missing-session", "sessions", "unknown.json", fmt.Sprintf(`{"pid":%d}`, os.Getpid()), false},
		{"unknown-ide", "ide", "123.lock", fmt.Sprintf(`{"pid":%d}`, os.Getpid()), false},
		{"malformed", "sessions", "broken.json", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, request := fixture(t)
			directory := filepath.Join(c.Store.ProfileDir(), tc.directory)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, tc.file), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			request.ID, request.Main, request.Model = "request", "opus", "opus"
			_, err := c.Acquire(request)
			if tc.allowed != (err == nil) {
				t.Fatalf("allowed=%v, routing error=%v", tc.allowed, err)
			}
			if !tc.allowed {
				if current := c.Store.CurrentAccountNumber(); current == nil || *current != "2" {
					t.Fatal("unsafe profile changed credentials")
				}
			}
		})
	}
}

func TestRegisterConsumesReservationWithNativeSessionID(t *testing.T) {
	c, request := fixture(t)
	if err := routingstate.With(c.Store.BackupDir(), func(state *routingstate.State) error {
		delete(state.History, "fable/session")
		state.Leases["fable/startup-launch"] = routingstate.Lease{Group: "fable", ID: "startup-launch", Session: "session", PID: request.PID, Account: "2", Model: "fable"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(request); err != nil {
		t.Fatal(err)
	}
	state, err := routingstate.Read(c.Store.BackupDir())
	if err != nil || len(state.Leases) != 0 || state.History["fable/session"].ID != request.Session {
		t.Fatalf("native reservation not consumed: %#v, %v", state, err)
	}
}

func TestLegacyStartupReservationPreventsRouting(t *testing.T) {
	c, request := fixture(t)
	if err := routingstate.With(c.Store.BackupDir(), func(state *routingstate.State) error {
		state.Sessions["fable/legacy-launch"] = routingstate.Session{Group: "fable", ID: "legacy-launch", PID: os.Getpid(), Model: "fable"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	request.ID, request.Main, request.Model = "request", "opus", "opus"
	if _, err := c.Acquire(request); err == nil {
		t.Fatal("legacy launch without a native registry entry did not block routing")
	}
	if current := c.Store.CurrentAccountNumber(); current == nil || *current != "2" {
		t.Fatal("legacy startup protection changed credentials")
	}
}
