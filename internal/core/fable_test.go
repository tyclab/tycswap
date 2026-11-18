// Tests for the core Fable methods (DESIGN A100): a switch of the Fable store
// by hand holds the Fable engine only.
package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
)

func fableCreds(access string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"ref-` + access + `","expiresAt":4102444800000,"subscriptionType":"max"}}`
}

// fableSwitcher: accounts 1 and 2 with default and Fable logins; the Fable
// store holds 1's; the root state file holds a hold of its own.
func fableSwitcher(t *testing.T) (*Switcher, *Switcher, string) {
	t.Helper()
	sw := newTestSwitcher(t)
	sw.Store.Platform = platform.Linux
	accounts := map[string]json.RawMessage{}
	for num, email := range map[string]string{"1": "one@example.com", "2": "two@example.com"} {
		accounts[num] = rawRecord(map[string]any{"email": email, "uuid": "u-" + num, "organizationUuid": "", "added": "t"})
		seedManaged(t, sw, num, email, oauthCreds("d"+num))
	}
	writeSeqDirect(t, sw, ptrInt(1), []int{1, 2}, accounts)
	seedLive(t, sw, "one@example.com", oauthCreds("d1"))
	fable, err := sw.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	for num, email := range map[string]string{"1": "one@example.com", "2": "two@example.com"} {
		if err := fable.WriteAccountCredentials(num, email, fableCreds("f"+num)); err != nil {
			t.Fatal(err)
		}
	}
	store := filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte(fableCreds("f1")), 0o600); err != nil {
		t.Fatal(err)
	}
	root := `{"schemaVersion":1,"lastSwitchAt":1,"lastSwitchTo":"1","lastSwitchBy":"hand"}`
	if err := os.WriteFile(sw.StatePath(), []byte(root), 0o600); err != nil {
		t.Fatal(err)
	}
	return sw, fable, root
}

func fableHold(t *testing.T, fable *Switcher) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(fable.StatePath())
	if os.IsNotExist(err) {
		return nil
	}
	var state map[string]any
	if err != nil || json.Unmarshal(raw, &state) != nil {
		t.Fatalf("Fable state %s (%v)", raw, err)
	}
	return state
}

func TestSwitchFableByHandStampsTheFableState(t *testing.T) {
	sw, fable, root := fableSwitcher(t)
	if fable.StatePath() != filepath.Join(sw.BackupDir(), "fable", "autoswitch_state.json") {
		t.Fatalf("Fable state path %s", fable.StatePath())
	}
	res, err := fable.SwitchFable("2", true)
	if err != nil || res["switched"] != true {
		t.Fatalf("SwitchFable = %v, %v", res, err)
	}
	state := fableHold(t, fable)
	if state["lastSwitchTo"] != "2" || state["lastSwitchBy"] != "hand" || state["lastSwitchAt"] == nil {
		t.Fatalf("Fable state %v, want a hand hold on 2", state)
	}
	if raw, _ := os.ReadFile(sw.StatePath()); string(raw) != root {
		t.Fatalf("root state changed: %s", raw)
	}
	if res, err := sw.SwitchFable("1", false); err != nil || res["switched"] != true {
		t.Fatalf("SwitchFable from the default switcher = %v, %v", res, err)
	}
	if got := fableHold(t, fable)["lastSwitchTo"]; got != "1" {
		t.Fatalf("hold on %v, want 1", got)
	}
}

func TestSwitchFableAlreadyActiveLeavesNoStamp(t *testing.T) {
	sw, fable, root := fableSwitcher(t)
	res, err := fable.SwitchFable("1", true)
	if err != nil || res["switched"] != false || res["reason"] != "already-active" {
		t.Fatalf("SwitchFable = %v, %v", res, err)
	}
	if state := fableHold(t, fable); state != nil {
		t.Fatalf("Fable state %v, want none", state)
	}
	if raw, _ := os.ReadFile(sw.StatePath()); string(raw) != root {
		t.Fatalf("root state changed: %s", raw)
	}
}

func TestEngineSwitchFableLeavesNoStamp(t *testing.T) {
	_, fable, _ := fableSwitcher(t)
	res, err := fable.EngineSwitchFable("2", true)
	if err != nil || res["switched"] != true {
		t.Fatalf("EngineSwitchFable = %v, %v", res, err)
	}
	if state := fableHold(t, fable); state != nil {
		t.Fatalf("Fable state %v, want no stamp", state)
	}
}

func TestEmptyFableTurnsTheStoreOff(t *testing.T) {
	sw, _, root := fableSwitcher(t)
	res, err := sw.EmptyFable(true)
	if err != nil || res["reason"] != "emptied" {
		t.Fatalf("EmptyFable = %v, %v", res, err)
	}
	if st := sw.FableStore(); st.On {
		t.Fatalf("FableStore %+v after EmptyFable", st)
	}
	if raw, _ := os.ReadFile(sw.StatePath()); string(raw) != root {
		t.Fatalf("root state changed: %s", raw)
	}
}

// --empty waits for the Fable state lock a Fable tick holds, so the tick
// installs no login under it.
func TestEmptyFableWaitsForTheFableStateLock(t *testing.T) {
	sw, fable, _ := fableSwitcher(t)
	lock := filelock.New(filepath.Join(filepath.Dir(fable.StatePath()), ".autoswitch_state.lock"), 0)
	if ok, err := lock.Acquire(0); !ok || err != nil {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	store := filepath.Join(paths.FableStoreDir(sw.BackupDir()), ".credentials.json")
	before, _ := os.ReadFile(store)
	done := make(chan error, 1)
	go func() {
		_, err := sw.EmptyFable(false)
		done <- err
	}()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if raw, _ := os.ReadFile(store); string(raw) != string(before) {
			t.Fatal("emptied while the Fable state lock was held")
		}
	}
	_ = lock.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := sw.FableStore(); st.On {
		t.Fatalf("FableStore %+v after EmptyFable", st)
	}
}

func TestSwitchFableWithoutFableLoginsCreatesNothing(t *testing.T) {
	sw := newTestSwitcher(t)
	sw.Store.Platform = platform.Linux
	writeSeqDirect(t, sw, ptrInt(1), []int{1}, map[string]json.RawMessage{
		"1": rawRecord(map[string]any{"email": "one@example.com", "uuid": "u-1", "organizationUuid": "", "added": "t"}),
	})
	seedManaged(t, sw, "1", "one@example.com", oauthCreds("d1"))
	if _, err := sw.SwitchFable("1", false); err == nil {
		t.Fatal("switched the Fable store onto an account without a Fable login")
	}
	if _, err := sw.EmptyFable(false); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{paths.FableScopeDir(sw.BackupDir()), paths.FableCredentialsDir(sw.BackupDir())} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s exists (%v)", dir, err)
		}
	}
}
