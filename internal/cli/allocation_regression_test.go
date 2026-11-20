package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

func TestSharedMythosStartAsksBeforeSpendingExcludedAllocation(t *testing.T) {
	sw, child := fableStartLogin(t, fableTickFixture{def: "1", off: true}, "1")
	setRecord(t, sw, "1", "mythos", true, store.ModelsKey, []string{"fable"})
	startResolve = func(string) (string, error) { return "/fake/claude", nil }
	previous := askPlacement
	t.Cleanup(func() { askPlacement = previous })
	var questions []string
	askPlacement = func(_ ioStreams, question string, _ bool) (bool, error) {
		questions = append(questions, question)
		return false, nil
	}
	code, _, message := runCLI(t, []string{"start", "--", "claude", "--model", "mythos"}, false, false)
	if code == 0 || child.execs != 0 || len(questions) != 1 || !strings.Contains(questions[0], "allocated to fable") || !strings.Contains(message, "deferred") {
		t.Fatalf("exit %d, launches %d, questions %q, message %q", code, child.execs, questions, message)
	}
}

func TestFableTickSkipsOpusOnlyAllocation(t *testing.T) {
	fx := atFableLimit
	fx.logins = []string{"2", "3", "4"}
	fx.usage = map[string][3]float64{"1": {10, 10, 30}, "2": {10, 20, 100}, "3": {5, 10, 10}, "4": {40, 50, 40}}
	sw, _ := fx.build(t)
	setRecord(t, sw, "3", store.ModelsKey, []string{"opus"})
	setRecord(t, sw, "4", store.ModelsKey, []string{"fable"})
	code, out, message := runCodex(t, "", "auto", "--once", "--json")
	_, fable, _ := splitByStore(t, out)
	ev := eventOf(fable, "switch")
	if code != 0 || ev == nil || fmt.Sprint(ev["to"].(map[string]any)["number"]) != "4" {
		t.Fatalf("exit %d, events %s, message %s", code, out, message)
	}
}

func TestIdleOpusKeepsAccountServingFableAvailable(t *testing.T) {
	sw, child := fableStartLogin(t, fableTickFixture{def: "1", live: "2", logins: []string{"2"}, usage: map[string][3]float64{"1": {20, 20, 20}, "2": {5, 10, 20}, "3": {10, 30, 20}}}, "2", "3")
	startResolve = func(string) (string, error) { return "/fake/claude", nil }
	code, _, message := runCLI(t, []string{"start", "--", "claude", "--model", "opus"}, false, false)
	data, err := sw.SequenceForUpdate()
	if err != nil {
		t.Fatal(err)
	}
	if code != 23 || child.execs != 1 || sw.ResolveOwnerSlot(data).Slot != "3" || sw.FableStore().Live.Slot != "2" {
		t.Fatalf("exit %d, default %s, Fable %s: %s", code, sw.ResolveOwnerSlot(data).Slot, sw.FableStore().Live.Slot, message)
	}
}

func TestDedicatedResumeRespectsScopedReset(t *testing.T) {
	sw, _ := fableStartLogin(t, fableTickFixture{def: "1", off: true}, "1")
	setRecord(t, sw, "1", "mythos", true, store.ModelsKey, []string{"opus", "mythos"})
	dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("1"), "")
	writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("synthetic-eligible", "synthetic-refresh"))
	const id = "11111111-2222-4333-8444-555555555555"
	if _, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), id, "opus", "mythos/"+filepath.Base(dir), sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{time.Hour, -time.Hour} {
		window := map[string]any{"name": "Opus", "pct": 100.0, "resets_at": sw.Clk.Now().Add(offset).Format(time.RFC3339)}
		u := map[string]any{"five_hour": map[string]any{"pct": 5.0}, "seven_day": map[string]any{"pct": 30.0}, "scoped": []any{window}}
		if err := sw.Usage.Record(map[string]usage.FetchRecord{"1": {Usage: u}}, map[string]usage.Identity{"1": {Email: fableEmail("1")}}); err != nil {
			t.Fatal(err)
		}
		placement, err := locateMythosLogin(sw.BackupDir(), "opus", "", id)
		if offset > 0 && (err == nil || placement.dir != "") || offset < 0 && (err != nil || placement.dir != dir) {
			t.Fatalf("reset offset %v: placement %+v, err %v", offset, placement, err)
		}
	}
}

func TestIdleOpusReservesLiveIndependentLogin(t *testing.T) {
	sw, child := fableStartLogin(t, fableTickFixture{def: "1", usage: map[string][3]float64{
		"1": {20, 20, 20}, "2": {5, 10, 20}, "3": {10, 30, 20},
	}}, "2", "3")
	startResolve = func(string) (string, error) { return "/fake/claude", nil }
	const id = "aaaaaaaa-bbbb-4111-8111-cccccccccccc"
	scope := store.MythosLoginDir(sw.BackupDir(), fableEmail("2"), "")
	writeFile(t, filepath.Join(sw.DefaultProfileDir(), "sessions", fmt.Sprint(os.Getpid())+".json"), fmt.Sprintf(`{"pid":%d,"sessionId":%q}`, os.Getpid(), id))
	if _, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), id, "mythos", "mythos/"+filepath.Base(scope), sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	code, _, message := runCLI(t, []string{"start", "--", "claude", "--model", "opus"}, false, false)
	data, err := sw.SequenceForUpdate()
	if err != nil {
		t.Fatal(err)
	}
	if code != 23 || child.execs != 1 || sw.ResolveOwnerSlot(data).Slot != "3" {
		t.Fatalf("exit %d, default %s: %s", code, sw.ResolveOwnerSlot(data).Slot, message)
	}
}
