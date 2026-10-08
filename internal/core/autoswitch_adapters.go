package core

// Shadows the store method: the frozen autoswitch.Switcher has no error return, so a write failure is logged.
func (sw *Switcher) BackfillAccountUUID(num, uuid string) {
	if err := sw.Store.BackfillAccountUUID(num, uuid); err != nil && sw.Store.Log != nil {
		sw.Store.Log.Warningf("BackfillAccountUUID(%s): %v", num, err)
	}
}
