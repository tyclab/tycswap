package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestNativeStartConfirmsActualSharedAllocation(t *testing.T) {
	for _, tc := range []struct {
		name, model, allocated, wantAccount string
		fable, off, allow, mismatch         bool
		marked                              []string
	}{
		{name: "already active", model: "opus", allocated: "mythos", wantAccount: "1", marked: []string{"1"}, mismatch: true, allow: true},
		{name: "no candidate or approval", model: "opus", allocated: "fable", wantAccount: "1", mismatch: true},
		{name: "automatic selection off", model: "opus", allocated: "fable", wantAccount: "1", marked: []string{"2"}, off: true, mismatch: true, allow: true},
		{name: "actual Fable store", model: "fable", allocated: "opus", wantAccount: "2", fable: true, off: true, mismatch: true},
		{name: "only default account excluded", model: "fable", allocated: "fable", wantAccount: "2", fable: true, off: true},
		{name: "eligible marked alternative", model: "opus", allocated: "opus", wantAccount: "2", marked: []string{"2", "3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := fableTickFixture{def: "1", off: tc.off}
			if tc.fable {
				fx.live, fx.logins = "2", []string{"2"}
			}
			sw, child := fableStartLogin(t, fx, tc.marked...)
			if tc.wantAccount != "1" {
				setRecord(t, sw, "1", store.ModelsKey, []string{"mythos"})
			}
			setRecord(t, sw, tc.wantAccount, store.ModelsKey, []string{tc.allocated})
			setRecord(t, sw, "3", store.ModelsKey, []string{"mythos"})
			startResolve = func(string) (string, error) { return "/verified/claude", nil }
			prev := askPlacement
			t.Cleanup(func() { askPlacement = prev })
			questions := 0
			askPlacement = func(_ ioStreams, question string, printMode bool) (bool, error) {
				questions++
				if !strings.Contains(question, "Account-"+tc.wantAccount+", allocated to "+tc.allocated) || !printMode {
					t.Fatalf("unexpected question %q, print=%v", question, printMode)
				}
				return tc.allow, nil
			}
			code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", tc.model, "-p", "hello"}, false, false)
			wantExec := 1
			if tc.mismatch && !tc.allow {
				wantExec = 0
			}
			if (questions == 1) != tc.mismatch || questions > 1 || child.execs != wantExec || (code == 23) != (wantExec == 1) {
				t.Fatalf("code=%d questions=%d execs=%d stderr=%q", code, questions, child.execs, stderr)
			}
		})
	}
}

func TestSharedAllocationRevalidatedBeforeExec(t *testing.T) {
	for _, change := range []string{"disabled", "allocation", "identity", "scoped exhaustion", "shared exhaustion"} {
		t.Run(change, func(t *testing.T) {
			sw, child := fableStartLogin(t, fableTickFixture{def: "1", off: true})
			startResolve = func(string) (string, error) { return "/verified/claude", nil }
			setRecord(t, sw, "1", store.ModelsKey, []string{"fable"})
			prev := askPlacement
			t.Cleanup(func() { askPlacement = prev })
			questions := 0
			askPlacement = func(_ ioStreams, _ string, _ bool) (bool, error) {
				questions++
				switch change {
				case "disabled":
					if err := sw.SetAccountDisabled("1", true); err != nil {
						t.Fatal(err)
					}
				case "allocation":
					setRecord(t, sw, "1", store.ModelsKey, []string{"mythos"})
				case "identity":
					setRecord(t, sw, "2", store.ModelsKey, []string{"fable"})
					if _, err := sw.SwitchTo("2", true); err != nil {
						t.Fatal(err)
					}
				default:
					u := map[string]any{"seven_day": map[string]any{"pct": 100.0}}
					if change == "scoped exhaustion" {
						u = map[string]any{"scoped": []any{map[string]any{"name": "Opus", "pct": 100.0}}}
					}
					if err := sw.Usage.Record(map[string]usage.FetchRecord{"1": {Usage: u}}, map[string]usage.Identity{"1": {Email: fableEmail("1")}}); err != nil {
						t.Fatal(err)
					}
				}
				return true, nil
			}
			code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "opus"}, false, false)
			if questions != 1 || child.execs != 0 || code != 1 || stderr == "" {
				t.Fatalf("code=%d questions=%d execs=%d stderr=%q", code, questions, child.execs, stderr)
			}
		})
	}
}

func TestPendingNativeFableSelectionPrefersAnOrdinaryCandidate(t *testing.T) {
	for _, name := range []string{"default", "Fable store", "fallback to default", "live session"} {
		t.Run(name, func(t *testing.T) {
			fx := fableTickFixture{def: "1", usage: map[string][3]float64{"2": {5, 10, 10}, "3": {20, 60, 30}}}
			if name == "Fable store" {
				fx.live, fx.logins = "1", []string{"1", "2", "3"}
			} else if name == "fallback to default" {
				fx.live, fx.logins = "1", []string{"1"}
			}
			sw, child := fableStartLogin(t, fx, "2", "3")
			setRecord(t, sw, "2", store.ModelsKey, []string{"opus"})
			setRecord(t, sw, "3", store.ModelsKey, []string{"fable"})
			if name == "live session" {
				pid := strconv.Itoa(os.Getpid())
				writeFile(t, filepath.Join(sw.DefaultProfileDir(), "sessions", pid+".json"), `{"pid":`+pid+`,"sessionId":"another"}`)
			}
			startResolve = func(string) (string, error) { return "/verified/claude", nil }
			code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "fable"}, false, false)
			if name == "live session" {
				if code != 1 || child.execs != 0 || !strings.Contains(stderr, "Use Account-2, allocated to opus") {
					t.Fatalf("active login preference changed: code=%d execs=%d stderr=%q", code, child.execs, stderr)
				}
				return
			}
			if code != 23 || child.execs != 1 {
				t.Fatalf("code=%d execs=%d stderr=%q", code, child.execs, stderr)
			}
			view := sw.Store
			if name == "Fable store" {
				var err error
				view, err = view.ForFable()
				if err != nil {
					t.Fatal(err)
				}
			}
			data, err := view.SequenceForUpdate()
			if err != nil || view.ResolveOwnerSlot(data).Slot != "3" {
				t.Fatalf("eligible Fable candidate was not selected: %v; stderr=%q", err, stderr)
			}
		})
	}
}

func TestExhaustedSharedAllocationIsNotOfferedAndReleasesItsHold(t *testing.T) {
	sw, child := fableStartLogin(t, fableTickFixture{def: "1", usage: map[string][3]float64{"1": {10, 100, 20}}}, "1")
	setRecord(t, sw, "1", store.ModelsKey, []string{"fable"})
	startResolve = func(string) (string, error) { return "/verified/claude", nil }
	code, _, stderr := runCLI(t, []string{"start", "--", "claude", "--model", "opus"}, false, false)
	if code != 1 || child.execs != 0 || !strings.Contains(stderr, "no remaining quota") || len(holdsIn(t, sw.StatePath())) != 0 {
		t.Fatalf("code=%d execs=%d stderr=%q", code, child.execs, stderr)
	}
}

func TestBoundResumeRechecksIndependentIdentityAndQuotaAfterApproval(t *testing.T) {
	for _, change := range []string{"identity", "quota"} {
		t.Run(change, func(t *testing.T) {
			sw, child := fableStartLogin(t, fableTickFixture{def: "1", off: true}, "1")
			setRecord(t, sw, "1", "mythos", true, store.ModelsKey, []string{"mythos"})
			dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("1"), "")
			writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("synthetic-eligible", "synthetic-refresh"))
			const id = "11111111-2222-4333-8444-555555555555"
			if _, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), id, "opus", "mythos/"+filepath.Base(dir), sw.Clk.Now()); err != nil {
				t.Fatal(err)
			}
			startResolve = func(string) (string, error) { return "/fake/claude", nil }
			prev := askPlacement
			t.Cleanup(func() { askPlacement = prev })
			asked := 0
			askPlacement = func(_ ioStreams, _ string, _ bool) (bool, error) {
				asked++
				if change == "identity" {
					setRecord(t, sw, "1", "organizationUuid", "another-fixture-org")
				} else {
					u := map[string]any{"five_hour": map[string]any{"pct": 100.0, "resets_at": sw.Clk.Now().Add(time.Hour).Format(time.RFC3339)}}
					if err := sw.Usage.Record(map[string]usage.FetchRecord{"1": {Usage: u}}, map[string]usage.Identity{"1": {Email: fableEmail("1")}}); err != nil {
						t.Fatal(err)
					}
				}
				return true, nil
			}
			code, _, msg := runCLI(t, []string{"start", "--", "claude", "--resume", id}, false, false)
			if asked != 1 || code == 0 || child.execs != 0 {
				t.Fatalf("asked %d, code %d, execs %d: %s", asked, code, child.execs, msg)
			}
		})
	}
}
