package cli

import (
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/reporting"
)

func sessionLogin(sw *core.Switcher, id string) (label, number string) {
	return placementFor(sw.Store, false).sessionLogin(id)
}

// sessionLogin is the login a session's link names and its owner, on p's inputs.
func (p *placementInputs) sessionLogin(id string) (label, number string) {
	rec, err := reporting.InspectSessionLink(p.view.BackupDir(), id, p.view.Clk.Now())
	if err != nil {
		if cerr.TypeName(err) == string(cerr.KindAccountNotFound) {
			return "", ""
		}
		return "unknown", "unknown"
	}
	return p.loginLabel(rec.Target)
}

func (d *dashboard) SessionLogin(id string) (label, number string) {
	return sessionLogin(d.sw, id)
}
