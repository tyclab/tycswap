package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/tyclab/tycswap/internal/statusline"
	"github.com/tyclab/tycswap/internal/usage"
)

func seedRoster(t *testing.T, s *Store) map[string]usage.Identity {
	t.Helper()
	one := 1
	rec := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	data := &SequenceData{
		ActiveAccountNumber: &one,
		Sequence:            []int{1, 2},
		Accounts: map[string]json.RawMessage{
			"1": rec(map[string]any{"email": "a@x.com", "organizationUuid": "org-a", "organizationName": "Org A"}),
			"2": rec(map[string]any{"email": "a@x.com", "organizationUuid": "org-b", "organizationName": "Org B"}),
		},
	}
	if err := s.WriteSequence(data); err != nil {
		t.Fatal(err)
	}
	return map[string]usage.Identity{"1": {Email: "a@x.com", OrgUUID: "org-a"}, "2": {Email: "a@x.com", OrgUUID: "org-b"}}
}

func readPublished(t *testing.T, s *Store) statusline.Document {
	t.Helper()
	b, err := os.ReadFile(statusline.Path(s.BackupDir()))
	if err != nil {
		t.Fatalf("statusline.json: %v", err)
	}
	var doc statusline.Document
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("statusline.json: %v\n%s", err, b)
	}
	return doc
}

func TestUsageMergeUpdatesStatusline(t *testing.T) {
	s := freshStore(t)
	ids := seedRoster(t, s)
	if u := readPublished(t, s).Accounts["a@x.com|org-a"].Usage; u != nil {
		t.Fatalf("usage before any merge = %+v", u)
	}
	lg := map[string]any{"five_hour": map[string]any{"pct": 6.0, "resets_at": "2026-07-17T12:00:00Z"}}
	if err := s.Usage.Record(map[string]usage.FetchRecord{"1": {Usage: lg}}, ids); err != nil {
		t.Fatal(err)
	}
	u := readPublished(t, s).Accounts["a@x.com|org-a"].Usage
	if u == nil || u.FiveHour == nil || u.FiveHour.Pct != 6 || u.FetchedAt != s.Clk.Now().Unix() {
		t.Fatalf("usage after a merge = %+v", u)
	}
	iv := 240.0
	if err := s.Usage.SetPollPlan(map[string]usage.PollPlan{"1": {IntervalS: &iv}}, ids); err != nil {
		t.Fatal(err)
	}
	if got := readPublished(t, s).Accounts["a@x.com|org-a"].Usage.PollIntervalS; got != 240 {
		t.Errorf("pollIntervalS after a plan = %d, want 240", got)
	}
}

// Roster writers (store lock) and usage merges (usage lock) at once, under
// -race: the file ends equal to a rebuild from the final sources.
func TestConcurrentRosterAndUsageWritersLoseNothing(t *testing.T) {
	s := freshStore(t)
	ids := seedRoster(t, s)
	const rounds = 15
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(2)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				err := s.WithRosterLocked(func(d *SequenceData) error {
					r := decodeRecord(d.Accounts["1"])
					r["alias"] = fmt.Sprintf("w%d-%d", w, i)
					d.Accounts["1"], _ = encodeRecord(r)
					return s.WriteSequence(d)
				})
				if err != nil {
					t.Errorf("roster writer: %v", err)
					return
				}
			}
		}(w)
		go func(slot string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				lg := map[string]any{"five_hour": map[string]any{"pct": float64(i)}}
				if err := s.Usage.Record(map[string]usage.FetchRecord{slot: {Usage: lg}}, ids); err != nil {
					t.Errorf("usage writer: %v", err)
					return
				}
			}
		}(fmt.Sprint(w + 1))
	}
	wg.Wait()

	in, ok, err := s.statuslineInput()
	if err != nil || !ok {
		t.Fatalf("statuslineInput = %v, %v", ok, err)
	}
	want, _ := json.Marshal(statusline.Build(in))
	got, _ := json.Marshal(readPublished(t, s))
	if string(got) != string(want) {
		t.Fatalf("published file lost an update:\n got  %s\n want %s", got, want)
	}
	for _, key := range []string{"a@x.com|org-a", "a@x.com|org-b"} {
		if u := readPublished(t, s).Accounts[key].Usage; u == nil || u.FiveHour.Pct != rounds-1 {
			t.Errorf("%s usage = %+v, want the last merge", key, u)
		}
	}
}
