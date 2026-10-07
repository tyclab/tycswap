package reporting

import (
	"strconv"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

type AccountSnapshot struct {
	Number   string
	Email    string
	OrgName  string
	OrgUUID  string
	IsActive bool
	Kind     string // "oauth" | "api_key"
	BaseURL  string
	// Switchable reports that the slot has both a stored credential and a stored
	// config backup, independent of the disabled flag (store.AccountIsSwitchable).
	Switchable       bool
	Usage            usage.UsageEntry
	Alias            string
	Disabled         bool // held out of auto-rotation (still a valid explicit target)
	RotationEligible bool
	AtLimit          bool
	LimitingWindows  []string
	Provider         string
}

const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
)

func providerOrDefault(p string) string {
	if p == "" {
		return ProviderClaude
	}
	return p
}

func (a AccountSnapshot) ProviderName() string { return providerOrDefault(a.Provider) }

// Key returns "<provider>:<number>", the identity that stays unique once more
// than one provider is listed (claude-swap PR #252 models.py
// AccountSnapshot.key). Slot numbers are per-provider, so "1" names a Claude
// account and a Codex one; any surface that shows both — the dashboard, the
// menu bar — must address rows by Key, never by Number, or a keystroke aimed
// at one provider lands on the other.
func (a AccountSnapshot) Key() string { return a.ProviderName() + ":" + a.Number }

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
	Provider     string
}

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
			BaseURL:          info.BaseURL,
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
