package cli

import (
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autostart"
)

// TestTrayAutostartReadsAtMostEvery30Seconds: the panel asks on every build;
// a read serves for 30 seconds, and a change makes the next read fresh.
func TestTrayAutostartReadsAtMostEvery30Seconds(t *testing.T) {
	reads, sets := 0, 0
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	on := false
	get, set := cachedAutostart(
		func() (bool, error) { reads++; return on, nil },
		func(v bool) error { sets++; on = v; return nil },
		func() time.Time { return now },
	)

	if v, err := get(); err != nil || v {
		t.Fatalf("first read = %v, %v", v, err)
	}
	get()
	if reads != 1 {
		t.Fatalf("two reads inside the window started the tool %d times, want once", reads)
	}
	now = now.Add(29 * time.Second)
	get()
	if reads != 1 {
		t.Errorf("a read at 29 s started the tool again (%d reads)", reads)
	}
	now = now.Add(time.Second)
	get()
	if reads != 2 {
		t.Errorf("a read at 30 s did not start the tool again (%d reads)", reads)
	}

	if err := set(true); err != nil || sets != 1 {
		t.Fatalf("set = %v after %d sets", err, sets)
	}
	if v, err := get(); err != nil || !v || reads != 3 {
		t.Errorf("read after a change = %v, %v after %d reads; want a fresh read saying on", v, err, reads)
	}
}

// TestAutostartStatusNamesTheTask: `app --autostart` names a logon task that
// owns start at login and keeps the Run value's wording.
func TestAutostartStatusNamesTheTask(t *testing.T) {
	for _, tc := range []struct {
		e    autostart.Entry
		want string
	}{
		{autostart.Entry{Enabled: true}, "Start at login: on"},
		{autostart.Entry{}, "Start at login: off"},
		{autostart.Entry{Enabled: true, Task: "io.github.tyclab.tycswap.remote"}, "Start at login: on (logon task io.github.tyclab.tycswap.remote)"},
		{autostart.Entry{Task: "io.github.tyclab.tycswap"}, "Start at login: off (logon task io.github.tyclab.tycswap)"},
	} {
		if got := autostartLine(tc.e); got != tc.want {
			t.Errorf("autostartLine(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}
