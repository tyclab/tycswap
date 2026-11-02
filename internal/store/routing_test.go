package store

import (
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
)

// The Opus / other group leaves Fable-capable accounts to Fable work; every
// other profile reserves nothing (DESIGN A71).
func TestReservedForFableOnlyInTheOpusGroup(t *testing.T) {
	s := freshStore(t)
	writeSequenceRaw(t, s, `{
  "activeAccountNumber": 1,
  "lastUpdated": "2026-07-17T09:00:00Z",
  "sequence": [1, 2, 3],
  "accounts": {
    "1": {"email": "max@example.com", "uuid": "", "organizationUuid": "org-one", "organizationName": "One", "added": "t", "subscriptionType": "max"},
    "2": {"email": "pro@example.com", "uuid": "", "organizationUuid": "org-two", "organizationName": "Two", "added": "t", "subscriptionType": "pro"},
    "3": {"email": "biz@example.com", "uuid": "", "organizationUuid": "org-three", "organizationName": "Three", "added": "t", "fableStart": false, "fableContinue": false}
  }
}`)
	opus, err := s.ForGroup(groups.Opus)
	if err != nil {
		t.Fatal(err)
	}
	fable, err := s.ForGroup(groups.Fable)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"1": true, "2": false, "3": false}
	for num, reserved := range want {
		if got := opus.ReservedForFable(num); got != reserved {
			t.Errorf("opus: ReservedForFable(%s) = %v, want %v", num, got, reserved)
		}
		for name, other := range map[string]*Store{"default": s, "fable": fable} {
			if other.ReservedForFable(num) {
				t.Errorf("%s: ReservedForFable(%s) = true, want nothing reserved", name, num)
			}
		}
	}
}
