// statusline.go — the store's side of <root>/statusline.json (DESIGN A54): it
// gathers the sources for one rebuild. Schema, builder and lock live in
// internal/statusline.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/tyclab/tycswap/internal/statusline"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/version"
)

// statuslinePublish is a seam for the failure test.
var statuslinePublish = statusline.Publish

// PublishStatusline rebuilds <root>/statusline.json from the roster and the
// usage table. The triggering write has already committed, so a failure (or a
// panic) is logged and swallowed. A Store without a backup root publishes
// nothing.
func (s *Store) PublishStatusline() {
	if s == nil || s.backupDir == "" {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.warnf("Status line state not written: %v", r)
		}
	}()
	if err := statuslinePublish(s.backupDir, s.statuslineInput); err != nil {
		s.warnf("Status line state not written: %v", err)
	}
}

// statuslineInput reads every source fresh and takes no lock: sequence.json
// and usage.json are replaced by rename, so each read sees one whole version.
// A present but unreadable roster keeps the published file (ok=false).
func (s *Store) statuslineInput() (statusline.Input, bool, error) {
	var data *SequenceData
	raw, err := os.ReadFile(s.SequenceFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return statusline.Input{}, false, nil
	}
	if err == nil && (json.Unmarshal(raw, &data) != nil || data == nil) {
		return statusline.Input{}, false, nil
	}
	now := time.Now()
	if s.Clk != nil {
		now = s.Clk.Now()
	}
	in := statusline.Input{ProducerVersion: version.Display(), Now: now, Usage: map[int]usage.UsageEntry{}}
	if data == nil {
		return in, true, nil
	}
	ids := map[string]usage.Identity{}
	slots := map[string]int{}
	for num, raw := range data.Accounts {
		slot, ok := atoiSlot(num)
		if !ok {
			continue
		}
		rec := decodeRecord(raw)
		r := statusline.Record{
			Slot:             slot,
			Email:            strField(rec, "email"),
			OrganizationUUID: strField(rec, "organizationUuid"),
			OrgName:          strField(rec, "organizationName"),
			Alias:            strField(rec, "alias"),
		}
		in.Records = append(in.Records, r)
		ids[num] = usage.Identity{Email: r.Email, OrgUUID: r.OrganizationUUID}
		slots[num] = slot
	}
	if s.Usage != nil && len(ids) > 0 {
		for num, e := range s.Usage.Entries(ids) {
			in.Usage[slots[num]] = e
		}
	}
	return in, true, nil
}
