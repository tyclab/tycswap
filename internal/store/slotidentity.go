package store

import (
	"sort"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/oauth"
)

// SlotForIdentity maps an identity the profile endpoint returned to a slot, or
// "". Email and organisation decide first, unless both sides carry different
// uuids (a recycled email); else the organisation-scoped account uuid does,
// since a stored email may be stale or an add-token placeholder.
func (s *Store) SlotForIdentity(data *SequenceData, id *oauth.Identity) string {
	if data == nil || id == nil {
		return ""
	}
	uuid := strings.TrimSpace(id.UUID)
	slot := ""
	if id.Email != "" {
		slot = s.FindAccountSlot(data, id.Email, id.OrgUUID)
	}
	if slot != "" && uuid != "" {
		rec, _ := recordFor(data, slot)
		if stored := strings.TrimSpace(strField(rec, "uuid")); stored != "" && stored != uuid {
			slot = ""
		}
	}
	if slot != "" || uuid == "" {
		return slot
	}
	for _, num := range sequenceOrderedSlots(data) {
		rec, _ := recordFor(data, num)
		if u := strField(rec, "uuid"); u != "" && u == uuid && strField(rec, "organizationUuid") == id.OrgUUID {
			return num
		}
	}
	return ""
}

// sequenceOrderedSlots lists the sequence's slots in order, then the others
// sorted, so the uuid scan never depends on map order.
func sequenceOrderedSlots(data *SequenceData) []string {
	out := make([]string, 0, len(data.Accounts))
	seen := map[string]bool{}
	for _, n := range data.Sequence {
		if k := strconv.Itoa(n); !seen[k] {
			if _, ok := data.Accounts[k]; ok {
				out = append(out, k)
				seen[k] = true
			}
		}
	}
	tail := make([]string, 0, len(data.Accounts))
	for k := range data.Accounts {
		if !seen[k] {
			tail = append(tail, k)
		}
	}
	sort.Strings(tail)
	return append(out, tail...)
}
