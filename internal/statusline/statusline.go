// Package statusline builds <store root>/statusline.json, the small file a
// Claude Code status line reads for the live account's label and quota
// (DESIGN A54). The file is derived state: every write rebuilds it wholly from
// the roster and the usage table under <root>/.statusline.lock and never reads
// it back. That lock is always the last one taken and nothing is acquired
// while it is held.
package statusline

import (
	"bytes"
	"encoding/json"
	"math"
	"path/filepath"
	"sort"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/termsafe"
	"github.com/tyclab/tycswap/internal/usage"
)

const (
	// Filename is the published file under the store root.
	Filename     = "statusline.json"
	lockFilename = ".statusline.lock"
	// SchemaVersion changes only on an incompatible change.
	SchemaVersion = 1
	Producer      = "tycswap"
	// DefaultPollIntervalS is published for an account with no poll plan.
	DefaultPollIntervalS = int64(usage.CandidateMaxIntervalS)
)

// Path is the published file under root.
func Path(root string) string { return filepath.Join(root, Filename) }

// Key is "<emailAddress>|<organizationUuid>", what a reader computes from
// ~/.claude.json's oauthAccount; a null organization is "". Email alone is not
// unique: one email may be a member of two organizations.
func Key(email, orgUUID string) string { return email + "|" + orgUUID }

// Record is one roster account. It carries no credential, so none can be
// published.
type Record struct {
	Slot                                    int
	Email, OrganizationUUID, OrgName, Alias string
}

// Input is everything one rebuild is made from.
type Input struct {
	ProducerVersion string
	Now             time.Time
	Records         []Record
	Usage           map[int]usage.UsageEntry // by slot
}

// Document is the published file.
type Document struct {
	SchemaVersion   int                `json:"schemaVersion"`
	Producer        string             `json:"producer"`
	ProducerVersion string             `json:"producerVersion"`
	WrittenAt       int64              `json:"writtenAt"`
	Accounts        map[string]Account `json:"accounts"`
}

// Account is one published account.
type Account struct {
	Slot  int    `json:"slot"`
	Label string `json:"label"`
	Usage *Usage `json:"usage"`
}

// Usage is an account's last good measurement; every time is epoch seconds.
type Usage struct {
	FetchedAt     int64    `json:"fetchedAt"`
	PollIntervalS int64    `json:"pollIntervalS"`
	FiveHour      *Window  `json:"fiveHour"`
	SevenDay      *Window  `json:"sevenDay"`
	Scoped        []Scoped `json:"scoped"`
}

// Window is the 5h or 7d window; ResetsAt is null when none was reported.
type Window struct {
	Pct      float64 `json:"pct"`
	ResetsAt *int64  `json:"resetsAt"`
}

// Scoped is a per-model weekly window.
type Scoped struct {
	Name     string  `json:"name"`
	Pct      float64 `json:"pct"`
	ResetsAt *int64  `json:"resetsAt"`
}

// Build makes the document from in, without I/O. A record without an email is
// skipped; two records with one identity publish the lower slot. The label is
// the alias, else the organization name, else the email, with terminal control
// characters stripped.
func Build(in Input) Document {
	doc := Document{
		SchemaVersion:   SchemaVersion,
		Producer:        Producer,
		ProducerVersion: in.ProducerVersion,
		WrittenAt:       in.Now.Unix(),
		Accounts:        map[string]Account{},
	}
	recs := append([]Record(nil), in.Records...)
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Slot < recs[j].Slot })
	for _, r := range recs {
		key := Key(r.Email, r.OrganizationUUID)
		if _, taken := doc.Accounts[key]; taken || r.Email == "" {
			continue
		}
		acct := Account{Slot: r.Slot}
		for _, v := range []string{r.Alias, r.OrgName, r.Email} {
			if acct.Label = termsafe.Strip(v); acct.Label != "" {
				break
			}
		}
		if e, ok := in.Usage[r.Slot]; ok {
			acct.Usage = buildUsage(e)
		}
		doc.Accounts[key] = acct
	}
	return doc
}

// buildUsage projects the last good measurement through oauth.NewUsage, as
// every other surface does; nil when there is none.
func buildUsage(e usage.UsageEntry) *Usage {
	if e.LastGood == nil || e.FetchedAt == nil || !finite(*e.FetchedAt) {
		return nil
	}
	u := &Usage{FetchedAt: int64(math.Floor(*e.FetchedAt)), PollIntervalS: DefaultPollIntervalS, Scoped: []Scoped{}}
	if p := e.PollIntervalS; p != nil && finite(*p) && *p > 0 {
		u.PollIntervalS = int64(math.Round(*p))
	}
	p := oauth.NewUsage(e.LastGood)
	if p == nil {
		return u
	}
	if w := p.FiveHour; w != nil {
		u.FiveHour = &Window{Pct: w.Pct, ResetsAt: epoch(w.ResetsAt)}
	}
	if w := p.SevenDay; w != nil {
		u.SevenDay = &Window{Pct: w.Pct, ResetsAt: epoch(w.ResetsAt)}
	}
	for _, s := range p.Scoped {
		u.Scoped = append(u.Scoped, Scoped{Name: termsafe.Strip(s.Name), Pct: s.Pct, ResetsAt: epoch(s.ResetsAt)})
	}
	return u
}

func epoch(resetsAt string) *int64 {
	if v, ok := oauth.ResetEpoch(resetsAt); ok {
		return &v
	}
	return nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Encode renders the file: two-space indent, no HTML escaping, one trailing
// newline.
func Encode(doc Document) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(doc)
	return buf.Bytes(), err
}

// lockTimeout bounds the wait for the status-line lock (see Publish).
const lockTimeout = time.Second

// Publish rebuilds <root>/statusline.json under the status-line lock. gather
// runs inside the lock, must read every source fresh and take no lock; every
// writer commits its source before calling Publish, so the last rebuild sees
// every committed change. ok=false keeps the file as it is. Publish runs
// inside switch and roster locks, so it waits at most lockTimeout: a skipped
// rebuild is redone by the next writer, a stalled switch is not.
func Publish(root string, gather func() (in Input, ok bool, err error)) error {
	return filelock.New(filepath.Join(root, lockFilename), lockTimeout).With(func() error {
		in, ok, err := gather()
		if err != nil || !ok {
			return err
		}
		b, err := Encode(Build(in))
		if err != nil {
			return err
		}
		return atomicfile.Write(Path(root), b, atomicfile.Opts{})
	})
}
