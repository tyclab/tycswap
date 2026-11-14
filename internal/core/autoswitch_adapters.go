package core

import "github.com/tyclab/tycswap/internal/switching"

func (sw *Switcher) PrepareRotationTarget(number string) error {
	return switching.ReleaseIdleLegacy(sw.Store, number)
}

func (sw *Switcher) PrepareRotationCandidates() {
	current := sw.CurrentAccountNumber()
	for _, number := range sw.SwitchableAccountNumbers() {
		if current != nil && *current == number {
			continue
		}
		if err := sw.PrepareRotationTarget(number); err != nil && sw.Log != nil {
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
