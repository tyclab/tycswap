package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

// A Mythos session that starts on the target's Mythos login after the policy
// was read makes the target rank 3: the default engine reads the ranks again
// right before its reserved switch and drops it (DESIGN A131).
func TestReservedHandOverRereadsTheRanksBeforeTheSwitch(t *testing.T) {
	sw, _ := (fableTickFixture{
		def: "1", live: "1", logins: []string{"1", "2"},
		usage: map[string][3]float64{
			"1": {10, 20, 20}, "2": {10, 20, 20},
			"3": {10, 100, 100}, "4": {10, 100, 100},
		},
	}).build(t)
	setRecord(t, sw, "2", "mythos", true)
	base := settings.Load(sw.BackupDir())
	policy := reporting.DefaultSessionPolicyResult(sw.Store, base, sw.Clk.Now())
	if policy.ReservedRank("1") != autoswitch.RankFableStore || policy.ReservedRank("2") != autoswitch.RankReserved {
		t.Fatalf("ranks %d and %d, want the Fable store's account and a reserved one", policy.ReservedRank("1"), policy.ReservedRank("2"))
	}
	var switched bool
	var dropped autoswitch.NoSwitchEvent
	engine := defaultEngine(sw, policy, func(e autoswitch.Event) {
		switch ev := e.(type) {
		case autoswitch.SwitchEvent:
			switched = switched || ev.Trigger == "reserved"
		case autoswitch.NoSwitchEvent:
			dropped = ev
		}
	}, false)

	const session = "aaaaaaaa-bbbb-4111-8111-cccccccccccc"
	dir := store.MythosLoginDir(sw.BackupDir(), fableEmail("2"), "")
	writeFile(t, filepath.Join(dir, ".credentials.json"), dryCreds("independent-access", "independent-refresh"))
	writeFile(t, filepath.Join(sw.DefaultProfileDir(), "sessions", fmt.Sprint(os.Getpid())+".json"), fmt.Sprintf(`{"pid":%d,"sessionId":%q}`, os.Getpid(), session))
	if _, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), session, "mythos", "mythos/"+filepath.Base(dir), sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	if fresh := reporting.DefaultSessionPolicyResult(sw.Store, base, sw.Clk.Now()); fresh.ReservedRank("2") != autoswitch.RankMythos {
		t.Fatalf("the new session left account 2 at rank %d", fresh.ReservedRank("2"))
	}
	outcome := engine.Tick()
	if switched || dropped.Reason != "reserved-no-target" {
		t.Fatalf("outcome %v, switched %v, last no-switch %+v: want the reserved switch dropped", outcome, switched, dropped)
	}
}
