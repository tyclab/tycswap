package reporting

import (
	"strconv"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

// Email, OrgName and Alias are stored values from exports, APIs and other tools' files: text renderers must termsafe.Strip them.
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
	Switchable bool
	Usage      usage.UsageEntry
	Alias      string
	Plan       string // plan name Claude Code reports for the login, "" when unknown
	StartLogin bool
	Mythos     bool // marked as an account that runs Mythos (DESIGN A106)
	Disabled   bool // held out of auto-rotation (still a valid explicit target)
	// Excludes the engine's transient quarantine: necessary, not sufficient, for an automatic pick.
	RotationEligible     bool
	OwnershipBlockReason string
	AtLimit              bool
	LimitingWindows      []string
	// "" means ProviderClaude so existing literals keep working; read it through ProviderName or Key.
	Provider string
	// Codex identifies accounts by a stable key even if two workspaces have the same display name.
	AccountKey string `json:"-"`
	// FableLogin is the account's Fable login; nil without one (DESIGN A104).
	FableLogin *FableLogin
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

func (a AccountSnapshot) DisplayTag() string { return displayTag(a.Email, a.OrgName) }

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
	// Fable is the Fable store as status --json shows it; nil while no
	// account has a Fable login (DESIGN A104).
	Fable map[string]any
}

func (s AccountsSnapshot) ProviderName() string { return providerOrDefault(s.Provider) }

// Snapshot takes one coherent snapshot of every managed account (spec 02§13
// accounts_snapshot). fetch has CollectUsageEntries semantics: nil makes every
// stale account eligible; a set restricts which accounts may be fetched.
// *core.Switcher's frozen AccountsSnapshot method delegates here.
func Snapshot(s *store.Store, fetch map[string]bool) *AccountsSnapshot {
	s = passStore(s, fetch)
	infos := BuildAccountsInfo(s)
	entries := CollectUsageEntries(s, infos, fetch)
	data, _ := s.ReadSequence()
	models := configuredModels(s)
	fable := readFableView(s, data)

	var activeNumber string
	accounts := make([]AccountSnapshot, 0, len(infos))
	for _, info := range infos {
		num := strconv.Itoa(info.Number)
		if info.IsActive {
			activeNumber = num
		}
		atLimit, limiting := atLimitFor(entries[num].DecisionValue(), models, s.Clk.Now())
		switchable := s.AccountIsSwitchable(num)
		disabled := recordDisabled(data, num)
		ownershipBlock := s.RotationOwnershipBlockReason(num)
		accounts = append(accounts, AccountSnapshot{
			Number:               num,
			Email:                info.Email,
			OrgName:              info.OrgName,
			OrgUUID:              info.OrgUUID,
			IsActive:             info.IsActive,
			Kind:                 s.AccountKindFor(num),
			BaseURL:              info.BaseURL,
			Switchable:           switchable,
			Usage:                entries[num],
			Alias:                info.Alias,
			Plan:                 info.Plan,
			Disabled:             disabled,
			StartLogin:           store.StartLoginFrom(data, num),
			Mythos:               store.MythosFrom(data, num),
			RotationEligible:     rotationEligible(data, switchable, disabled, s.AccountKindFor(num)) && ownershipBlock == "",
			OwnershipBlockReason: ownershipBlock,
			AtLimit:              atLimit,
			LimitingWindows:      limiting,
			Provider:             ProviderClaude,
			FableLogin:           fable.login(num),
		})
	}
	snap := &AccountsSnapshot{
		ActiveNumber: activeNumber,
		Accounts:     accounts,
		TakenAt:      clock.Seconds(s.Clk),
		Provider:     ProviderClaude,
	}
	if fable != nil {
		snap.Fable = fable.object(s, data, entries[fable.live.Slot])
	}
	return snap
}

// A nil roster carries no disabled information, so no slot is eligible: fail closed like store.RotationEligible (DESIGN A19).
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
