package store

import (
	"encoding/json"
	"testing"
)

func TestSortedSlotKeysTotalOrder(t *testing.T) {
	want := []string{"3", "15", "2abc"}
	for iter := 0; iter < 50; iter++ {
		data := &SequenceData{Accounts: map[string]json.RawMessage{
			"15":   json.RawMessage(`{}`),
			"3":    json.RawMessage(`{}`),
			"2abc": json.RawMessage(`{}`),
		}}
		got := sortedSlotKeys(data)
		if len(got) != len(want) {
			t.Fatalf("sortedSlotKeys = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("sortedSlotKeys = %v, want %v", got, want)
			}
		}
	}
}
