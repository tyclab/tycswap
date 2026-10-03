package reporting

import (
	"reflect"
	"testing"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/testutil"
)

// TestResolvePollInputsKeysOnTheLowestBar: with no engine pinning the poll
// plan, cadence follows settings.json, and the one figure the planner
// escalates on is the lowest bar in force; the per-model bar joins only while
// autoswitch.model counts something (DESIGN A34). A file that still carries
// the single legacy "threshold" key seeds the 7d bar with it.
func TestResolvePollInputsKeysOnTheLowestBar(t *testing.T) {
	ClearPollPolicyInputs()
	t.Cleanup(ClearPollPolicyInputs)
	s := newStore(t, testutil.FixedClock(t, "2026-07-17T09:00:00Z"), &oauth.FakeClient{})
	for _, tc := range []struct {
		name, body string
		want       float64
		models     []string
	}{
		{"defaults", `{}`, 85, nil},
		{"7d lowest", `{"autoswitch":{"fiveHourThreshold":90,"sevenDayThreshold":80}}`, 80, nil},
		{"model bar not in force", `{"autoswitch":{"fiveHourThreshold":90,"sevenDayThreshold":95,"modelThreshold":60}}`, 90, nil},
		{"model bar in force", `{"autoswitch":{"fiveHourThreshold":90,"sevenDayThreshold":95,"modelThreshold":60,"model":"all"}}`, 60, []string{"all"}},
		{"legacy threshold", `{"autoswitch":{"threshold":70}}`, 70, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeSettings(t, s, tc.body)
			threshold, models := resolvePollInputs(s)
			if threshold != tc.want || !reflect.DeepEqual(append([]string{}, models...), append([]string{}, tc.models...)) {
				t.Errorf("resolvePollInputs = %v %v, want %v %v", threshold, models, tc.want, tc.models)
			}
		})
	}
}
