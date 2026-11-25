package autoswitch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/settings"
)

// scopedFake is the fake with BeginReads, naming every call made to prepare
// or switch to a target while the reads were still grouped.
type scopedFake struct {
	*fakeSwitcher
	open, begun int
	inScope     []string
}

func (f *scopedFake) BeginReads() func() {
	f.begun++
	f.open++
	return func() { f.open-- }
}

func (f *scopedFake) note(call string) {
	if f.open > 0 {
		f.inScope = append(f.inScope, call)
	}
}

func (f *scopedFake) LiveSessionPidsFor(num, email string) []int {
	f.note("LiveSessionPidsFor")
	return f.fakeSwitcher.LiveSessionPidsFor(num, email)
}

func (f *scopedFake) RefreshBackupGuarded(ctx context.Context, c oauth.Client, num, email, held string) oauth.RefreshOutcome {
	f.note("RefreshBackupGuarded")
	return f.fakeSwitcher.RefreshBackupGuarded(ctx, c, num, email, held)
}

func (f *scopedFake) SwitchTo(num string, jsonOut bool) (map[string]any, error) {
	f.note("SwitchTo")
	return f.fakeSwitcher.SwitchTo(num, jsonOut)
}

// A tick's reads are one operation up to the candidate loop: a target is
// prepared, rechecked and switched to after they end (DESIGN A116), and the
// next tick groups its own.
func TestTickEndsItsReadsBeforePreparingATarget(t *testing.T) {
	clk := newClk()
	f := &scopedFake{fakeSwitcher: twoAccounts(clk, dictEntry(usageOf(10, 98)), dictEntry(usageOf(10, 10)))}
	f.creds["2"] = nearExpiryCreds(clk, "r2")
	rec := &recorder{}
	e := NewEngine(f, settings.Default(), rec.on, false, WithClock(clk), WithRNG(func() float64 { return 0.5 }),
		WithStatePath(filepath.Join(t.TempDir(), StateFilename)),
		WithOAuthClient(fakeOAuth(func(string) oauth.RefreshOutcome {
			return oauth.RefreshOutcome{Credentials: farFutureCreds(clk, "r2b")}
		})))
	if got := e.Tick(); got != Switched {
		t.Fatalf("outcome = %v, kinds %v; want a switch", got, rec.kinds())
	}
	if len(f.inScope) != 0 || f.open != 0 || f.begun != 1 {
		t.Fatalf("called %v with the reads grouped; %d begun, %d still open", f.inScope, f.begun, f.open)
	}
	if e.Tick(); f.begun != 2 || f.open != 0 {
		t.Fatalf("second tick: %d begun, %d still open", f.begun, f.open)
	}
}
