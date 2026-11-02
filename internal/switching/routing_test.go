package switching

import (
	"os"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/routingstate"
)

func TestRequestReservationGuardsManualSwitchAndRelease(t *testing.T) {
	s := groupFixture(t)
	fable := scope(t, s, groups.Fable)
	if _, err := SwitchTo(fable, "1", true, false); err != nil {
		t.Fatal(err)
	}
	if err := routingstate.With(s.BackupDir(), func(state *routingstate.State) error {
		state.Sessions["fable/session"] = routingstate.Session{ID: "session", Group: "fable", PID: os.Getpid(), Model: "fable"}
		state.Leases["fable/request"] = routingstate.Lease{ID: "request", Session: "session", Group: "fable", PID: os.Getpid(), Account: "1", Model: "fable"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(fable, "2", true, false); err == nil {
		t.Fatal("manual switch ignored request")
	}
	if _, err := ReleaseGroup(fable, false); err == nil {
		t.Fatal("release ignored request")
	}
	if _, err := SwitchTo(fable, "1", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(scope(t, s, groups.Opus), "2", true, false); err != nil {
		t.Fatal("independent profile blocked:", err)
	}
}

func TestRuntimeModelCanUseNonFableAccountInSameProfile(t *testing.T) {
	s := groupFixture(t)
	fable := scope(t, s, groups.Fable)
	if _, err := SwitchTo(fable, "1", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(s, "2", true, false); err != nil {
		t.Fatal(err)
	}
	if err := routingstate.With(s.BackupDir(), func(*routingstate.State) error {
		_, err := SwitchTo(fable.WithRoutingModel(groups.Opus).WithRoutingLockHeld(), "3", true, false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if *fable.CurrentAccountNumber() != "3" {
		t.Fatal("runtime route did not select Pro")
	}
}
