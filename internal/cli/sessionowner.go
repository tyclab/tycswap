package cli

import (
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
)

func sessionLogin(sw *core.Switcher, id string) (label, number string) {
	rec, err := reporting.InspectSessionLink(sw.BackupDir(), id, sw.Clk.Now())
	if err != nil {
		if cerr.TypeName(err) == string(cerr.KindAccountNotFound) {
			return "", ""
		}
		return "unknown", "unknown"
	}
	return reporting.SessionLoginOwner(sw.Store, rec.Target)
}

func (d *dashboard) SessionLogin(id string) (label, number string) {
	return sessionLogin(d.sw, id)
}
