package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

// countingStore is the fixture with accounts 1 and 2 able to run Fable, the
// given cached usage (5h, 7d, Fable per account) and one live default session
// running model.
func countingStore(t *testing.T, model string, usage map[string][3]float64) *core.Switcher {
	t.Helper()
	sw := fixtureSwitcher(t)
	data, err := sw.SequenceForUpdate()
	if err != nil {
		t.Fatal(err)
	}
	for _, num := range []string{"1", "2"} {
		var rec map[string]any
		if err := json.Unmarshal(data.Accounts[num], &rec); err != nil {
			t.Fatal(err)
		}
		rec["fableContinue"] = true
		data.Accounts[num], _ = json.Marshal(rec)
	}
	if err := sw.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := map[string]any{}
	for num, u := range usage {
		email := map[string]string{"1": "alice@example.com", "2": "bob@example.com"}[num]
		reset := now.Add(3 * time.Hour).UTC().Format(time.RFC3339)
		rows[num] = map[string]any{"email": email, "organizationUuid": "", "fetchedAt": float64(now.Unix()) - 30, "lastGood": map[string]any{
			"five_hour": map[string]any{"pct": u[0], "resets_at": reset},
			"seven_day": map[string]any{"pct": u[1], "resets_at": reset},
			"scoped":    []any{map[string]any{"name": "Fable", "pct": u[2], "resets_at": reset}},
		}}
	}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "accounts": rows})
	if err := os.MkdirAll(filepath.Join(sw.BackupDir(), "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sw.BackupDir(), "cache", "usage.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(sw.DefaultProfileDir(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": "s-live"})
	if err := os.WriteFile(filepath.Join(sessions, strconv.Itoa(os.Getpid())+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), "s-live", model, now); err != nil {
		t.Fatal(err)
	}
	return sw
}

// The dashboard's state says what the scheduled ticks count now, from the
// default-session policy itself: a Fable session some account can serve,
// one none can serve, and no Fable session with the accounts left to Fable
// (DESIGN A91). A settings.json that still holds autoswitch.model counts the
// same (A97).
func TestAutoViewReportsCounting(t *testing.T) {
	room := map[string][3]float64{"1": {10, 20, 30}, "2": {10, 20, 30}}
	spent := map[string][3]float64{"1": {10, 20, 100}, "2": {10, 20, 100}}
	for name, tc := range map[string]struct {
		model, setting string
		usage          map[string][3]float64
		want           web.CountingView
	}{
		"retired-model-key": {model: "claude-fable-5-1", setting: "Fable", usage: spent,
			want: web.CountingView{Mode: "sessions", Models: []string{}, Sessions: []string{"fable"}, Unservable: []string{"Fable"}, Reserved: []string{}}},
		"fable-counted": {model: "claude-fable-5-1", usage: room,
			want: web.CountingView{Mode: "sessions", Models: []string{"Fable"}, Sessions: []string{"fable"}, Unservable: []string{}, Reserved: []string{}}},
		"fable-unservable": {model: "claude-fable-5-1", usage: spent,
			want: web.CountingView{Mode: "sessions", Models: []string{}, Sessions: []string{"fable"}, Unservable: []string{"Fable"}, Reserved: []string{}}},
		"no-fable-session": {model: "claude-opus-5-5", usage: room,
			want: web.CountingView{Mode: "sessions", Models: []string{}, Sessions: []string{"opus"}, Unservable: []string{}, Reserved: []string{"1", "2"}}},
	} {
		t.Run(name, func(t *testing.T) {
			sw := countingStore(t, tc.model, tc.usage)
			body := `{"schemaVersion":1}`
			if tc.setting != "" {
				body = `{"schemaVersion":1,"autoswitch":{"model":"` + tc.setting + `"}}`
			}
			if err := os.WriteFile(settings.SettingsPath(sw.BackupDir()), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			a := newAutoFacade(sw, nil)
			a.managedBy = testOwner
			got := a.View().Counting
			if got == nil || !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("counting = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Under a scheduler the Auto tab shows when a tick last ran, from the engine
// state the ticks write, not "not running".
func TestAutoViewReportsTheLastTick(t *testing.T) {
	sw := fixtureSwitcher(t)
	a := newAutoFacade(sw, nil)
	a.managedBy = testOwner
	if got := a.View().LastTickAt; got != nil {
		t.Fatalf("last tick %v before any tick", *got)
	}
	state := `{"schemaVersion": 1, "lastSeenLive": "1", "lastSeenAt": 1763251200.5}`
	if err := os.WriteFile(autoswitch.StatePath(sw.BackupDir()), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := a.View().LastTickAt; got == nil || *got != 1763251200.5 {
		t.Fatalf("last tick = %v, want 1763251200.5", got)
	}
}
