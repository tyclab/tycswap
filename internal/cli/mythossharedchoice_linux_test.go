package cli

import (
	"github.com/tyclab/tycswap/internal/reporting"
	"testing"
)

func TestHandChoiceMovesAcceptedSharedMythos(t *testing.T) {
	sw, _, to := mythosTickFixture(t, 20, 30, true)
	link, err := reporting.CreateSessionLink(sw.BackupDir(), sw.DefaultProfileDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = reporting.BindSessionLink(sw.BackupDir(), tickSessionA, link, "mythos", "", sw.Clk.Now()); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := runSub(t, "switch", "--store", "mythos", "3", "--json"); code != 0 {
		t.Fatal(code, errb)
	}
	requireLinkTarget(t, link, to)
}
