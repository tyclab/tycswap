package lifecycle

import (
	"errors"
	"slices"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
)

func withFableAllocationEdit(s *store.Store, identifier string, edit func(*store.SequenceData, string) error) error {
	var account groups.Account
	err := s.WithRosterLocked(func(data *store.SequenceData) error {
		num, email, org, err := s.ResolveAccountFrom(data, identifier)
		if err != nil {
			return err
		}
		account = groups.Account{Number: num, Email: email, OrgUUID: org}
		return edit(data, num)
	})
	if err != nil {
		return err
	}
	f, err := s.ForFable()
	if err != nil || !f.HasLiveLogin() {
		return nil
	}
	local := *f
	local.OAuth = nil
	data, err := local.SequenceForUpdate()
	if err != nil || !outsideFableAllocation(&local, data, account) {
		return err
	}
	return autoswitch.HandSwitch(local.StatePath(), s.Clk, nil, func() (string, error) {
		current, err := local.SequenceForUpdate()
		if err != nil || !outsideFableAllocation(&local, current, account) {
			return "", err
		}
		if err := local.SetFableAllocationParked(true); err != nil {
			return "", err
		}
		busy, err := SelectedStoreBusy(&local)
		if err != nil || busy {
			return "", err
		}
		return "", parkChangedFable(&local, account)
	})
}

func outsideFableAllocation(f *store.Store, data *store.SequenceData, a groups.Account) bool {
	if data == nil || f.FindAccountSlot(data, a.Email, a.OrgUUID) != a.Number || f.ResolveOwnerSlot(data).Slot != a.Number {
		return false
	}
	set, explicit := store.ModelsFrom(data, a.Number)
	return store.DisabledFrom(data, a.Number) || !f.FableEntitlement(data, a.Number).Capable || explicit && !slices.Contains(set, "fable")
}

func parkChangedFable(f *store.Store, account groups.Account) error {
	_, err := switching.WithdrawLogin(f, account, "", func(current *store.SequenceData) error {
		if !outsideFableAllocation(f, current, account) {
			return errWithdrawalSuperseded
		}
		return requireIdleFable(f)
	})
	if errors.Is(err, errWithdrawalSuperseded) {
		return nil
	}
	return err
}

func requireIdleFable(f *store.Store) error {
	busy, err := SelectedStoreBusy(f)
	if err != nil {
		return err
	}
	if busy {
		return errors.New("the Fable login is still in use; its changed allocation is waiting for an idle store")
	}
	return nil
}

// PrepareRecoveredFableStart returns the stable account identity the native
// command will use. The PID that will exec (or wait for) it is registered
// under the native credential lock, before exposing the selected login.
func PrepareRecoveredFableStart(s *store.Store, account, bound string) (string, error) {
	def := s.WithoutLineageWrites()
	def.OAuth = nil
	f, err := def.ForFable()
	if err != nil {
		return "", err
	}
	var identity string
	err = autoswitch.HandSwitch(f.StatePath(), f.Clk, nil, func() (string, error) {
		data, err := def.SequenceForUpdate()
		if err != nil {
			return "", err
		}
		if account != "" {
			account, _, _, err = def.ResolveAccountFrom(data, account)
			if err != nil {
				return "", err
			}
		}
		parked, err := store.FableAllocationParked(f.BackupDir())
		if err != nil {
			return "", err
		}
		readers, err := store.ReadFableReaders(f.BackupDir())
		if err != nil {
			return "", err
		}
		if !parked && readers.Identity == "" && bound == "" {
			return "", nil
		}
		live, absent, err := f.Creds.ReadActive()
		if err != nil {
			return "", err
		}
		if absent {
			return "", errors.New("the Fable credential store is unavailable")
		}
		selected := f.ResolveOwnerSlot(data).Slot
		if live != "" && selected == "" {
			return "", errors.New("cannot verify the recovered Fable login")
		}
		if live == "" && !parked {
			return "", errors.New("the original Fable login is off; the session cannot resume")
		}
		targetFor := func(roster *store.SequenceData) groups.Account {
			serving := ""
			if live != "" {
				serving = readers.Identity
			}
			return recoveredCandidate(def, f, roster, account, selected, bound, serving)
		}
		target := targetFor(data)
		if target.Number == "" && live != "" && bound == "" {
			if err := requireIdleFable(f); err != nil {
				return "", err
			}
			_, email, org, err := def.ResolveAccountFrom(data, selected)
			if err != nil {
				return "", err
			}
			old := groups.Account{Number: selected, Email: email, OrgUUID: org}
			changed, err := switching.WithdrawLogin(f, old, "", func(current *store.SequenceData) error {
				if targetFor(current).Number != "" || f.FindAccountSlot(current, email, org) != selected || f.ResolveOwnerSlot(current).Slot != selected {
					return errors.New("the selected Fable login changed; retry the start")
				}
				return requireIdleFable(f)
			})
			if err != nil {
				return "", err
			}
			if !changed {
				return "", errors.New("the selected Fable login changed during parking; retry the start")
			}
			live, selected = "", ""
			target = targetFor(data)
		}
		if target.Number == "" {
			return "", errors.New("no eligible independently signed-in Fable startup account has quota for this session")
		}
		digest := store.AccountIdentityDigest(target.Email, target.OrgUUID)
		check := func(_ *store.Store, current *store.SequenceData) error {
			if targetFor(current) != target {
				return errors.New("the eligible Fable login changed; retry the start")
			}
			now, absent, err := f.Creds.ReadActive()
			if err != nil {
				return err
			}
			if absent || (live == "") != (now == "") {
				return errors.New("the selected Fable login changed; retry the start")
			}
			if live != "" {
				owner := f.ResolveOwnerSlot(current)
				if owner.Slot != target.Number || owner.Source == store.SourceLastInstall || owner.Source == store.SourceConfig {
					return errors.New("cannot verify the recovered Fable login's owner")
				}
			} else if err := requireIdleFable(f); err != nil {
				return err
			}
			return nil
		}
		if live != "" {
			err = f.WithFableLocks(func() error {
				current, err := def.SequenceForUpdate()
				if err != nil {
					return err
				}
				if err := check(f, current); err != nil {
					return err
				}
				return f.PinFableReader(digest)
			})
		} else {
			err = switching.RecoverFable(f, target.Number, digest, check)
		}
		if err != nil {
			return "", err
		}
		if err := f.SetFableAllocationParked(false); err != nil {
			return "", err
		}
		identity = digest
		if live != "" {
			return "", nil
		}
		return target.Number, nil
	})
	return identity, err
}

func recoveredCandidate(def, f *store.Store, data *store.SequenceData, account, selected, bound, serving string) groups.Account {
	return withdrawalTarget(def, f, data, func(num string) bool {
		if account == "" && !store.StartLoginFrom(data, num) || account != "" && account != num || selected != "" && selected != num {
			return false
		}
		plan := def.FableEntitlement(data, num).Plan
		if !groups.Entitlement(groups.Fable, groups.Start, plan, nil, nil).Allowed {
			return false
		}
		if bound == "" && serving == "" {
			return true
		}
		_, email, org, err := def.ResolveAccountFrom(data, num)
		if err != nil {
			return false
		}
		digest := store.AccountIdentityDigest(email, org)
		return (bound == "" || bound == digest) && (serving == "" || serving == digest)
	})
}

// UpdateParkedFableReady records the current eligible choice. A scheduled
// tick never installs a grant into an empty store an unregistered native
// client might still hold open. Native startup revalidates the choice.
func UpdateParkedFableReady(s *store.Store) error {
	parked, err := store.FableAllocationParked(s.BackupDir())
	if err != nil || !parked {
		return err
	}
	def := s.WithoutLineageWrites()
	def.OAuth = nil
	f, err := def.ForFable()
	if err != nil {
		return err
	}
	return autoswitch.HandSwitch(f.StatePath(), s.Clk, nil, func() (string, error) {
		p, err := store.ReadFableParking(s.BackupDir())
		if err != nil || p == nil || !settings.Load(s.BackupDir()).Enabled {
			return "", err
		}
		data, err := def.SequenceForUpdate()
		if err != nil {
			return "", err
		}
		next := store.FableParking{}
		live, unavailable, err := f.Creds.ReadActive()
		if err != nil {
			return "", err
		}
		busy, err := SelectedStoreBusy(f)
		if err != nil {
			return "", err
		}
		if live == "" && !unavailable && !busy {
			target := recoveredCandidate(def, f, data, "", "", "", "")
			if target.Number != "" {
				next.ReadyAccount = target.Number
				next.Identity = store.AccountIdentityDigest(target.Email, target.OrgUUID)
			}
		}
		if next == *p {
			return "", nil
		}
		return "", f.WriteFableParking(next)
	})
}
