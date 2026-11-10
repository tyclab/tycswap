// autoswitch.enabled at the top of every tick (DESIGN A83): off, a tick
// touches neither the switcher nor the state file; a dry run still decides.

package autoswitch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/settings"
)

func off() bool { return false }

// overBar is a store whose active account is over its weekly bar, with a
// candidate well below it: an enabled tick switches.
func overBar() (*fakeSwitcher, *recorder, *clock.Fake) {
	clk := newClk()
	return twoAccounts(clk, dictEntry(usageOf(10, 98)), dictEntry(usageOf(10, 10))), &recorder{}, clk
}

func TestTickDisabledDoesNothing(t *testing.T) {
	f, rec, clk := overBar()
	switches := 0
	f.switchTo = func(*fakeSwitcher, string) (map[string]any, error) {
		switches++
		return nil, errors.New("switched while off")
	}
	dir := t.TempDir()
	e := build(t, f, settings.Default(), rec, clk, false,
		WithStatePath(filepath.Join(dir, StateFilename)), WithEnabled(off))
	if got := e.Tick(); got != NoAction {
		t.Fatalf("outcome = %v, want NoAction (kinds %v)", got, rec.kinds())
	}
	if kinds := rec.kinds(); len(kinds) != 1 || kinds[0] != "no-switch" {
		t.Fatalf("events = %v, want exactly one no-switch", kinds)
	}
	ns := rec.events[0].(NoSwitchEvent)
	if ns.Reason != "disabled" || ns.Detail != "auto-switch is off; tycswap auto on turns it on" {
		t.Fatalf("no-switch = %+v", ns)
	}
	if len(f.fetchCalls) != 0 || switches != 0 {
		t.Fatalf("a disabled tick fetched usage %v times and switched %d times", len(f.fetchCalls), switches)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("a disabled tick wrote beside the state file: %v %v", entries, err)
	}
}

// tickOverBar runs one tick over a fresh overBar store and returns what it did.
func tickOverBar(t *testing.T, dryRun bool, opts ...Option) ([]Event, TickOutcome, string) {
	t.Helper()
	f, rec, clk := overBar()
	outcome := build(t, f, settings.Default(), rec, clk, dryRun, opts...).Tick()
	return rec.events, outcome, deref(f.current)
}

func TestDryTickWhenDisabledDecides(t *testing.T) {
	want, wantOutcome, _ := tickOverBar(t, true)
	if wantOutcome != Switched {
		t.Fatalf("the enabled dry tick decided %v; the store must want a switch", wantOutcome)
	}
	got, outcome, current := tickOverBar(t, true, WithEnabled(off))
	if len(got) == 0 {
		t.Fatal("a disabled dry tick emitted nothing")
	}
	if warn, ok := got[0].(ConfigWarningEvent); !ok || warn.Message != "auto-switch is off; this dry run shows what it would do" {
		t.Fatalf("first event = %#v, want the config-warning", got[0])
	}
	if !reflect.DeepEqual(got[1:], want) || outcome != wantOutcome {
		t.Fatalf("disabled dry tick = %v %#v, want the enabled one's %v %#v", outcome, got[1:], wantOutcome, want)
	}
	if current != "1" {
		t.Fatalf("a dry tick moved the login to %s", current)
	}
}

func TestTickEnabledUnchanged(t *testing.T) {
	want, wantOutcome, wantCurrent := tickOverBar(t, false)
	got, outcome, current := tickOverBar(t, false, WithEnabled(func() bool { return true }))
	if !reflect.DeepEqual(got, want) || outcome != wantOutcome || current != wantCurrent {
		t.Fatalf("enabled tick = %v %s %#v, want %v %s %#v", outcome, current, got, wantOutcome, wantCurrent, want)
	}
	if wantOutcome != Switched {
		t.Fatalf("the reference tick decided %v, want a switch", wantOutcome)
	}
}
