package lifecycle

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/usage"
)

var errWithdrawalSuperseded = errors.New("the login or account changed before withdrawal")

// RetryDisabledSelections completes an earlier disable once the last client
// releases its login. The caller supplies the live auto-switch setting so it
// can be checked again under the native write locks.
func RetryDisabledSelections(s *store.Store, enabled func() bool, report func(bool, groups.Account, groups.Account)) error {
	data, err := s.SequenceForUpdate()
	if err != nil {
		return err
	}
	for _, n := range data.Sequence {
		if store.DisabledFrom(data, strconv.Itoa(n)) {
			return withdrawDisabled(s, nil, enabled, report)
		}
	}
	return nil
}

func withdrawDisabled(s *store.Store, requested *groups.Account, enabled func() bool, report func(bool, groups.Account, groups.Account)) error {
	def := s.WithoutLineageWrites()
	def.OAuth = nil
	scopes := []*store.Store{s}
	var result error
	if f, err := s.ForFable(); err == nil {
		if live, _, err := f.Creds.ReadActive(); err != nil {
			result = fmt.Errorf("cannot verify the secondary login: %w", err)
		} else if live != "" {
			scopes = append(scopes, f)
		}
	}
	for _, scope := range scopes {
		view := scope.WithoutLineageWrites()
		view.OAuth = nil
		data, err := view.SequenceForUpdate()
		if err != nil {
			return err
		}
		num := view.ResolveOwnerSlot(data).Slot
		if num == "" || !store.DisabledFrom(data, num) {
			continue
		}
		_, email, org, err := view.ResolveAccountFrom(data, num)
		if err != nil {
			return err
		}
		account := groups.Account{Number: num, Email: email, OrgUUID: org}
		if requested != nil && *requested != account {
			continue
		}
		err = autoswitch.HandSwitch(scope.StatePath(), scope.Clk, nil, func() (string, error) {
			data, err := view.SequenceForUpdate()
			if err != nil {
				return "", err
			}
			if !enabled() || !selectedDisabled(view, data, account) {
				return "", nil
			}
			busy, err := SelectedStoreBusy(view)
			if err != nil || busy {
				return "", err
			}
			target := withdrawalTarget(def, view, data)
			check := func(current *store.SequenceData) error {
				if !enabled() || !selectedDisabled(view, current, account) {
					return errWithdrawalSuperseded
				}
				if withdrawalTarget(def, view, current) != target {
					return errors.New("the eligible replacement changed before withdrawal; retry disable")
				}
				if busy, err := SelectedStoreBusy(view); err != nil {
					return err
				} else if busy {
					return errWithdrawalSuperseded
				}
				return nil
			}
			local := *scope
			local.OAuth = nil
			changed, err := switching.WithdrawLogin(&local, account, target.Number, check)
			if err != nil {
				if errors.Is(err, errWithdrawalSuperseded) {
					return "", nil
				}
				return "", err
			}
			if !changed {
				return "", nil
			}
			report(scope.IsFable(), account, target)
			return target.Number, nil
		})
		result = errors.Join(result, err)
	}
	return result
}

func reportWithdrawal(fable bool, from, to groups.Account) {
	name := "default"
	if fable {
		name = "Fable"
	}
	if to.Number == "" {
		emitLine(fmt.Sprintf("  No eligible %s login is available. Account-%s is saved.", name, from.Number))
	} else {
		emitLine(fmt.Sprintf("  The %s login is now Account-%s.", name, to.Number))
	}
}

func selectedDisabled(s *store.Store, data *store.SequenceData, a groups.Account) bool {
	return data != nil && store.DisabledFrom(data, a.Number) && s.FindAccountSlot(data, a.Email, a.OrgUUID) == a.Number && s.ResolveOwnerSlot(data).Slot == a.Number
}

// SelectedStoreBusy reports whether a managed launch or a live client may
// depend on this store. Callers recheck under the native write locks.
func SelectedStoreBusy(s *store.Store) (bool, error) {
	if held, err := autoswitch.StartHeld(s.StatePath(), s.Clk.Now()); held || err != nil {
		return held, err
	}
	if !s.IsFable() {
		if other, err := s.ForFable(); err == nil {
			if held, err := autoswitch.StartHeld(other.StatePath(), s.Clk.Now()); held || err != nil {
				return held, err
			}
		}
	}
	pids, err := store.LiveProfilePIDs(s.DefaultProfileDir())
	if err != nil || len(pids) == 0 {
		return false, err
	}
	// A default-login change writes shared Claude configuration. Only the
	// file-based secondary scope can move independently of another live scope.
	if !s.IsFable() {
		return true, nil
	}
	sessions, ides, err := procdetect.GetRunningInstancesErr(s.DefaultProfileDir())
	if err != nil || len(ides) != 0 {
		return true, err
	}
	scopes := reporting.SessionStores(s.BackupDir())
	for _, p := range pids {
		identified := false
		for _, session := range sessions {
			if session.PID != p {
				continue
			}
			scope, ok := scopes[session.SessionID]
			digest, independent := strings.CutPrefix(scope, "mythos/")
			if !ok || scope != "" && (!independent || len(digest) != 64) {
				return true, nil
			}
			identified = true
		}
		if !identified {
			return true, nil
		}
	}
	return false, nil
}

func withdrawalTarget(def, scope *store.Store, data *store.SequenceData) groups.Account {
	family := "opus"
	if scope.IsFable() {
		family = "fable"
	}
	var candidates []string
	ids := map[string]usage.Identity{}
	held := autoswitch.ReadQuarantine(scope.StatePath())

	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if _, bad := held[num]; bad || !def.RotationEligible(data, num) || def.EnsureAccountAvailable(num) != nil {
			continue
		}
		if scope.IsFable() && (!def.FableEntitlement(data, num).Capable || !def.HasFableLogin(num)) {
			continue
		}
		if families, set := store.ModelsFrom(data, num); set && !slices.Contains(families, family) {
			continue
		}
		_, email, org, err := def.ResolveAccountFrom(data, num)
		if err != nil {
			continue
		}
		ids[num] = usage.Identity{Email: email, OrgUUID: org}
		candidates = append(candidates, num)
	}
	ordered := autoswitch.InitialPlacementTargets(candidates, def.Usage.Entries(ids), settings.Load(def.BackupDir()), family, def.Clk.Now())
	if len(ordered) == 0 {
		return groups.Account{}
	}
	id := ids[ordered[0]]
	return groups.Account{Number: ordered[0], Email: id.Email, OrgUUID: id.OrgUUID}
}
