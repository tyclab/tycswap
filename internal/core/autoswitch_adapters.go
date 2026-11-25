package core

import (
	"errors"

	"github.com/tyclab/tycswap/internal/switching"
)

func (sw *Switcher) PrepareRotationTarget(number string) error {
	return switching.ReleaseIdleLegacy(sw.Store, number)
}

// PrepareRotationCandidates releases the idle former profiles of the accounts
// automatic rotation may take. Which ones it tries is one read (DESIGN A116);
// each release reads afresh.
func (sw *Switcher) PrepareRotationCandidates() {
	view := sw.Store.OneRead()
	current := view.CurrentAccountNumber()
	for _, number := range view.SwitchableAccountNumbers() {
		if current != nil && *current == number || !view.MayReleaseLegacy(number) {
			continue
		}
		switch err := sw.PrepareRotationTarget(number); {
		case err == nil || sw.Log == nil:
		case errors.Is(err, switching.ErrStillHeld):
			sw.Log.Debugf("Account-%s session release held: %v", number, err)
		default:
			sw.Log.Warningf("Account-%s session release held: %v", number, err)
		}
	}
}

// Shadows the store method: the frozen autoswitch.Switcher has no error return, so a write failure is logged.
func (sw *Switcher) BackfillAccountUUID(num, uuid string) {
	if err := sw.Store.BackfillAccountUUID(num, uuid); err != nil && sw.Store.Log != nil {
		sw.Store.Log.Warningf("BackfillAccountUUID(%s): %v", num, err)
	}
}
