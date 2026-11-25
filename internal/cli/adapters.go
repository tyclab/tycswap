package cli

import (
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/transfer"
	"github.com/tyclab/tycswap/internal/usage"
)

// ---- autoswitch.Switcher (DESIGN A13, FROZEN) ------------------------------

// autoswitchAdapter is the one-line fix documented at length in
// internal/core/autoswitch_adapters.go: *core.Switcher plus a shadowing
// no-error ReadAccountCredentials satisfies autoswitch.Switcher in full. Every
// other method is promoted from *core.Switcher unchanged.
type autoswitchAdapter struct{ *core.Switcher }

// ReadAccountCredentials shadows the embedded (string, error) form with the
// no-error shape autoswitch.Switcher pins; a read failure folds into "",
// matching Python's _read_account_credentials which never raises (spec 05§12).
func (a autoswitchAdapter) ReadAccountCredentials(num, email string) string {
	v, _ := a.Switcher.ReadAccountCredentials(num, email)
	return v
}

// SwitchTo shadows the hand switch: the engine and `tycswap start` stamp
// their own switch under the state lock they hold (DESIGN A94).
func (a autoswitchAdapter) SwitchTo(id string, jsonOut bool) (map[string]any, error) {
	return a.Switcher.EngineSwitchTo(id, jsonOut)
}

// RecordedSwitch is the slot the roster names active and when the roster was
// last written, as epoch seconds: every tycswap switch writes both, so the
// engine can tell a login made elsewhere (live-login-changed).
func (a autoswitchAdapter) RecordedSwitch() (string, float64) {
	data, err := a.ReadSequence()
	if err != nil || data == nil || data.ActiveAccountNumber == nil {
		return "", 0
	}
	slot := strconv.Itoa(*data.ActiveAccountNumber)
	at, err := time.Parse(time.RFC3339, data.LastUpdated)
	if err != nil {
		return slot, 0
	}
	return slot, float64(at.Unix())
}

var _ autoswitch.Switcher = autoswitchAdapter{}

// tickAdapter is an engine's autoswitchAdapter whose ticks each read the
// ownership inputs once (DESIGN A116): between BeginReads and its end the
// reads that ask about ownership go to one OneRead copy of the store. Every
// other method, and every write, is the plain store's.
type tickAdapter struct {
	autoswitchAdapter
	view *atomic.Pointer[core.Switcher]
}

var _ autoswitch.Switcher = tickAdapter{}

func newTickAdapter(sw *core.Switcher) tickAdapter {
	return tickAdapter{autoswitchAdapter{sw}, &atomic.Pointer[core.Switcher]{}}
}

func (a tickAdapter) BeginReads() (end func()) { return beginReads(a.view, a.Switcher) }

func (a tickAdapter) reads() *core.Switcher { return readsOf(a.view, a.Switcher) }

// beginReads puts one OneRead copy of sw's store in view until end; an
// adapter without a view has no read scope.
func beginReads(view *atomic.Pointer[core.Switcher], sw *core.Switcher) (end func()) {
	if view == nil {
		return func() {}
	}
	view.Store(&core.Switcher{Store: sw.Store.OneRead()})
	return func() { view.Store(nil) }
}

// readsOf is the switcher the scoped reads go to: view's copy while a scope
// is open, else sw.
func readsOf(view *atomic.Pointer[core.Switcher], sw *core.Switcher) *core.Switcher {
	if view != nil {
		if v := view.Load(); v != nil {
			return v
		}
	}
	return sw
}

func (a tickAdapter) CurrentAccountNumber() *string      { return a.reads().CurrentAccountNumber() }
func (a tickAdapter) HasLiveLogin() bool                 { return a.reads().HasLiveLogin() }
func (a tickAdapter) SwitchableAccountNumbers() []string { return a.reads().SwitchableAccountNumbers() }
func (a tickAdapter) RotationBlockReasons() map[string]string {
	return a.reads().RotationBlockReasons()
}
func (a tickAdapter) PrepareRotationCandidates() { a.reads().PrepareRotationCandidates() }

func (a tickAdapter) UsageEntriesByAccount(fetch map[string]bool) map[string]usage.UsageEntry {
	return a.reads().UsageEntriesByAccount(fetch)
}

// transferAdapter provides the transfer.Accounts surface over *core.Switcher.
// Methods core already promotes with the exact frozen shape (ResolveAccount's
// siblings ReadAccountCredentials/WriteAccountCredentials/WriteAccountConfig,
// LiveSessionPidsFor, SetupDirectories, InitSequenceFile, BackupDir, Platform)
// come through embedding; the rest are explicit adapters over the exported
// *store.Store, mirroring transfer/accounts.go's per-method mapping.
type transferAdapter struct{ *core.Switcher }

var _ transfer.Accounts = transferAdapter{}

func fromStoreSeq(d *store.SequenceData) *transfer.SequenceData {
	if d == nil {
		return nil
	}
	return &transfer.SequenceData{
		ActiveAccountNumber: d.ActiveAccountNumber,
		LastUpdated:         d.LastUpdated,
		Sequence:            d.Sequence,
		Accounts:            d.Accounts,
	}
}

func toStoreSeq(d *transfer.SequenceData) *store.SequenceData {
	if d == nil {
		return nil
	}
	return &store.SequenceData{
		ActiveAccountNumber: d.ActiveAccountNumber,
		LastUpdated:         d.LastUpdated,
		Sequence:            d.Sequence,
		Accounts:            d.Accounts,
	}
}

func (t transferAdapter) MigratedSequence() (*transfer.SequenceData, error) {
	d, err := t.Store.SequenceMigrated()
	return fromStoreSeq(d), err
}

// MigratedSequenceForUpdate is the classified entry read transfer's write paths
// start from: absent yields an empty roster, unreadable refuses (spec 07§3).
// transfer.Import calls it from INSIDE its own write-pass FileLock, so this
// adapter must stay lock-free — store.MigratedSequenceForUpdateLocked takes no lock,
// and the store lock is non-reentrant (DESIGN A20 RULE 4, §2.8 F2).
func (t transferAdapter) MigratedSequenceForUpdate() (*transfer.SequenceData, error) {
	d, err := t.Store.MigratedSequenceForUpdateLocked()
	if err != nil {
		return nil, err
	}
	return fromStoreSeq(d), nil
}

func (t transferAdapter) Sequence() (*transfer.SequenceData, error) {
	d, err := t.Store.ReadSequence()
	return fromStoreSeq(d), err
}

func (t transferAdapter) WriteSequence(data *transfer.SequenceData) error {
	return t.Store.WriteSequence(toStoreSeq(data))
}

func (t transferAdapter) ResolveSlot(id string) (string, error) {
	num, _, _, err := t.Store.ResolveAccount(id)
	if err != nil {
		if cerr.TypeName(err) == string(cerr.KindAccountNotFound) {
			return "", nil
		}
		return "", err
	}
	return num, nil
}

// CurrentAccount is the account the live default login belongs to, by the
// resolved live slot (DESIGN A68): a Fable session writes its own account into
// ~/.claude.json. While Fable logins exist, a slot only the config names is no
// evidence, so no account counts as live (DESIGN A101).
func (t transferAdapter) CurrentAccount() (email, orgUUID string, ok bool) {
	data, err := t.Store.ReadSequence()
	if err != nil || data == nil {
		return "", "", false
	}
	live := t.Store.ResolveLiveSlot(data)
	if live.Slot == "" || live.Source == store.SourceConfig && t.Store.AccountKindFor(live.Slot) == "oauth" && t.Store.HasFableLogins() {
		return "", "", false
	}
	id := t.Store.AccountIdentity(live.Slot)
	return id["email"], id["organizationUuid"], true
}

// ReadActiveCredentials returns the live active credential ("" when none),
// dropping the keychain-unavailable flag (spec 07§3 == _read_credentials).
func (t transferAdapter) ReadActiveCredentials() (string, error) {
	v, _, err := t.Store.Creds.ReadActive()
	return v, err
}

func (t transferAdapter) ReadActiveConfig() (string, bool, error) {
	b, err := os.ReadFile(paths.GetGlobalConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(b), true, nil
}

// DropFableLogin removes the Fable login under (num, email): an account
// imported into a slot it did not hold cannot own it (DESIGN A101).
func (t transferAdapter) DropFableLogin(num, email string) error {
	f, err := t.Store.ForFable()
	if err != nil {
		return nil
	}
	if _, err := os.Stat(f.CredentialsDir); err != nil {
		return nil
	}
	return f.Creds.DeleteBackupStrict(num, email)
}

func (t transferAdapter) ReadAccountConfig(num, email string) (string, error) {
	return t.Store.ReadAccountConfig(num, email)
}

func (t transferAdapter) usageIdentity(num, email, orgUUID string) map[string]usage.Identity {
	return map[string]usage.Identity{num: {Email: email, OrgUUID: orgUUID}}
}

// TokenDead reports whether a slot's stored credential is quarantined dead,
// identity-guarded (spec 07§3 == usage.entries[slot].token_dead()).
func (t transferAdapter) TokenDead(num, email, orgUUID string) bool {
	ids := t.usageIdentity(num, email, orgUUID)
	return t.Store.Usage.Entries(ids)[num].TokenDead()
}

func (t transferAdapter) ClearDeadToken(num, email, orgUUID string) error {
	ids := t.usageIdentity(num, email, orgUUID)
	return t.Store.Usage.ClearDeadToken([]string{num}, ids)
}

func (t transferAdapter) Timestamp() string {
	return t.Store.Clk.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func (t transferAdapter) Platform() platform.Platform { return t.Store.Platform }
