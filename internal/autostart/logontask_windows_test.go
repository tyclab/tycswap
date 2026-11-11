package autostart

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
)

// TestLogonTaskThroughTheRealTool drives a throwaway task, under a random
// name, through the real schtasks.exe: CI only, since it registers a task
// for the user running it. It touches no task it did not create, and a fake
// stands in for the Run value.
func TestLogonTaskThroughTheRealTool(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("registers a scheduled task; runs where CI is set")
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	label := "tycswap-test-" + hex.EncodeToString(b)
	k := useFakes(t, &fakeSchtasks{})
	runSchtasks = execSchtasks
	c := Config{Home: t.TempDir(), Exe: `C:\tycswap.exe`, Label: label, GOOS: "windows"}

	// Absent first: the tool's "no such task" leaves the Run value in charge,
	// and /Create below cannot replace a task someone else owns.
	if got, err := Status(c); err != nil || got != (Entry{}) {
		t.Fatalf("Status before /Create = %+v, %v; want no task", got, err)
	}
	out, code, err := execSchtasks("/Create", "/TN", taskName(c), "/TR", "cmd.exe /c exit 0", "/SC", "ONCE", "/ST", "23:59")
	if err != nil || code != 0 {
		t.Fatalf("schtasks /Create: exit %#x, %v: %s", code, err, out)
	}
	t.Cleanup(func() {
		if out, code, err := execSchtasks("/Delete", "/TN", taskName(c), "/F"); err != nil || code != 0 {
			t.Errorf("schtasks /Delete: exit %#x, %v: %s", code, err, out)
		}
	})

	want := func(step string, on bool) {
		t.Helper()
		if got, err := Status(c); err != nil || got != (Entry{Enabled: on, Task: label}) {
			t.Fatalf("%s: Status = %+v, %v; want enabled %v", step, got, err, on)
		}
	}
	want("created", true)
	if err := Disable(c); err != nil {
		t.Fatal(err)
	}
	want("after Disable", false)
	if err := Enable(c); err != nil {
		t.Fatal(err)
	}
	want("after Enable", true)
	for _, call := range k.calls {
		if call == "set" {
			t.Errorf("a Run value was written beside the task: %v", k.calls)
		}
	}
}
