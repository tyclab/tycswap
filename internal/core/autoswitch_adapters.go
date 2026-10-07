package core

func (sw *Switcher) BackfillAccountUUID(num, uuid string) {
	if err := sw.Store.BackfillAccountUUID(num, uuid); err != nil && sw.Store.Log != nil {
		sw.Store.Log.Warningf("BackfillAccountUUID(%s): %v", num, err)
	}
}
