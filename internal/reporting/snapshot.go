// snapshot.go — the coherent one-pass AccountsSnapshot the TUI/menubar consume
// through tui.Facade (spec 09§6.1, DESIGN A11), plus the pure usage-fetch-stamp
// read the watch view diffs to flash refreshed rows.
//
// Implements spec 02§13 / models.py (accounts_snapshot, AccountSnapshot,
// AccountsSnapshot, usage_fetch_stamps): metadata, active detection, and usage
// entries all come from a single BuildAccountsInfo + CollectUsageEntries pass so
// a consumer never sees a list and usage table that disagree.
package reporting

import (
	"strconv"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

// AccountSnapshot is one managed account as seen by interactive UIs (spec
// models.py AccountSnapshot). Usage is the store-backed read model: display code
// reads Usage.LastGood/AgeS directly (may show old data, age-annotated), while
// Usage.Sentinel carries derived states that replace the bars entirely.
//
// Email, OrgName and Alias are the stored values, so a JSON consumer sees
// what the store holds; a text renderer passes them through termsafe.Strip,
// since they come from exports, APIs and other tools' files.
type AccountSnapshot struct {
	Number   string
	Email    string
	OrgName  string
	OrgUUID  string
	IsActive bool
	Kind     string // "oauth" | "api_key"
	// Switchable reports that the slot has both a stored credential and a stored
	// config backup, independent of the disabled flag (store.AccountIsSwitchable).
	Switchable bool
	Usage      usage.UsageEntry
	Alias      string
	Disabled   bool // held out of auto-rotation (still a valid explicit target)
	// RotationEligible is store.RotationEligible's rule — Switchable && !Disabled
	// and not an API-key account (DESIGN A33), and false outright when the
	// roster could not be read (see rotationEligible).
	// The snapshot carries all three so no consumer has to re-derive or re-AND
	// them (DESIGN A18); the two inputs are not otherwise recoverable from the
	// conjunction. It is eligibility for AUTOMATIC selection only, and it does NOT
	// account for the auto-switch engine's transient quarantine (that lives in
	// autoswitch_state.json): it is necessary but not sufficient for "the engine
	// could pick this slot now". The per-reason skip warnings that distinguish
	// "(disabled)" from "no stored credentials/config" are produced in
	// switching/switch.go, which reads the store directly rather than this
	// snapshot.
	RotationEligible bool
	// AtLimit is set when the account's decision-grade usage sits at/over a rate
	// limit, folding in the per-model weekly windows configured via
	// autoswitch.model; LimitingWindows names the limiting windows in
	// RelevantWindows order (Go-side additive extension, DESIGN A15).
	AtLimit         bool
	LimitingWindows []string
	// Provider names the CLI this account belongs to: ProviderClaude or
	// ProviderCodex (claude-swap PR #252 models.py). The zero value "" means
	// ProviderClaude, so every existing construction site — and every test that
	// builds a snapshot literal — keeps working untouched; only multi-provider
	// consumers (the TUI grouping, the auto loop) ever read it, and they go
	// through ProviderName or Key rather than the raw field.
	Provider string
}

// Provider names carried by AccountSnapshot.Provider and AccountsSnapshot.Provider.
const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
)

// providerOrDefault maps the zero value to ProviderClaude.
func providerOrDefault(p string) string {
	if p == "" {
		return ProviderClaude
	}
	return p
}

// ProviderName returns the account's provider, treating "" as ProviderClaude.
func (a AccountSnapshot) ProviderName() string { return providerOrDefault(a.Provider) }

// Key returns "<provider>:<number>", the identity that stays unique once more
// than one provider is listed (claude-swap PR #252 models.py
// AccountSnapshot.key). Slot numbers are per-provider, so "1" names a Claude
// account and a Codex one; any surface that shows both — the dashboard, the
// menu bar — must address rows by Key, never by Number, or a keystroke aimed
// at one provider lands on the other.
func (a AccountSnapshot) Key() string { return a.ProviderName() + ":" + a.Number }

// DisplayTag returns the org tag for display: the org name, or "personal".
func (a AccountSnapshot) DisplayTag() string { return displayTag(a.OrgName) }

// AccountsSnapshot is the coherent one-pass view of every managed account (spec
// models.py AccountsSnapshot). ActiveNumber is "" when there is no active
// managed account. The type name is fixed by the frozen tui.Facade interface
// (DESIGN A13), so the producing free function is Snapshot rather than a
// same-named function.
type AccountsSnapshot struct {
	ActiveNumber string
	Accounts     []AccountSnapshot
	TakenAt      float64
	// Provider names whose accounts these are (claude-swap PR #252 models.py);
	// "" means ProviderClaude, as on AccountSnapshot.
	Provider string
}

// ProviderName returns the snapshot's provider, treating "" as ProviderClaude.
func (s AccountsSnapshot) ProviderName() string { return providerOrDefault(s.Provider) }

// Snapshot takes one coherent snapshot of every managed account (spec 02§13
// accounts_snapshot). fetch has CollectUsageEntries semantics: nil makes every
// stale account eligible; a set restricts which accounts may be fetched.
// *core.Switcher's frozen AccountsSnapshot method delegates here.
func Snapshot(s *store.Store, fetch map[string]bool) *AccountsSnapshot {
	infos := BuildAccountsInfo(s)
	entries := CollectUsageEntries(s, infos, fetch)
	data, _ := s.ReadSequence()
	models := configuredModels(s)

	var activeNumber string
	accounts := make([]AccountSnapshot, 0, len(infos))
	for _, info := range infos {
		num := strconv.Itoa(info.Number)
		if info.IsActive {
			activeNumber = num
		}
		atLimit, limiting := atLimitFor(entries[num].DecisionValue(), models)
		switchable := s.AccountIsSwitchable(num)
		disabled := recordDisabled(data, num)
		accounts = append(accounts, AccountSnapshot{
			Number:           num,
			Email:            info.Email,
			OrgName:          info.OrgName,
			OrgUUID:          info.OrgUUID,
			IsActive:         info.IsActive,
			Kind:             s.AccountKindFor(num),
			Switchable:       switchable,
			Usage:            entries[num],
			Alias:            info.Alias,
			Disabled:         disabled,
			RotationEligible: rotationEligible(data, switchable, disabled, s.AccountKindFor(num)),
			AtLimit:          atLimit,
			LimitingWindows:  limiting,
			Provider:         ProviderClaude,
		})
	}
	return &AccountsSnapshot{
		ActiveNumber: activeNumber,
		Accounts:     accounts,
		TakenAt:      clock.Seconds(s.Clk),
		Provider:     ProviderClaude,
	}
}

// rotationEligible is store.RotationEligible's rule spelled out over values the
// one-pass snapshot already holds, so the field costs no second credential and
// config read per account (DESIGN A19 names this the one inlined derivation).
// Being a copy, it fails closed on the same input the owner does: data is the
// ONLY source for the disabled half, so a nil roster carries no disabled
// information at all — it is not "nothing is disabled" — and no slot is
// eligible. The two halves do not come from one read (switchable is answered by
// a per-account re-read inside store.AccountIsSwitchable, disabled from the one
// roster in hand), and rows are already in hand from BuildAccountsInfo's earlier
// read, so a nil roster here is reachable while the other half still answers
// yes. ANDing a fresh answer with a blind one would rank a slot the user
// deliberately held out of rotation.
func rotationEligible(data *store.SequenceData, switchable, disabled bool, kind string) bool {
	return data != nil && switchable && !disabled && kind != "api_key"
}

// UsageFetchStamps returns each managed slot's fetchedAt from the usage store — a
// pure file read, no fetching or credential access (spec 02§13
// usage_fetch_stamps). The TUI watch view diffs consecutive stamps to flash rows
// whose usage just refreshed. A slot with no stamp yields a nil pointer.
func UsageFetchStamps(s *store.Store) map[string]*float64 {
	data, _ := s.ReadSequence()
	if data == nil {
		return map[string]*float64{}
	}
	identities := make(map[string]usage.Identity, len(data.Accounts))
	for num, raw := range data.Accounts {
		rec := decodeRecord(raw)
		identities[num] = usage.Identity{Email: recStr(rec, "email"), OrgUUID: recStr(rec, "organizationUuid")}
	}
	out := make(map[string]*float64, len(identities))
	for num, entry := range s.Usage.Entries(identities) {
		out[num] = entry.FetchedAt
	}
	return out
}
