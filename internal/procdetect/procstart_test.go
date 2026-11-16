package procdetect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// realProcStart is field 22 of this process's /proc stat, or a skip where
// there is no /proc.
func realProcStart(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip("no /proc on this system")
	}
	s := string(raw)
	return strings.Fields(s[strings.LastIndexByte(s, ')')+1:])[19]
}

func sessionPIDs(t *testing.T, records ...map[string]any) []int {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, rec := range records {
		writeJSON(t, filepath.Join(dir, "sessions", strconv.Itoa(i)+".json"), rec)
	}
	var pids []int
	for _, s := range ListSessions(dir) {
		pids = append(pids, s.PID)
	}
	return pids
}

// SES-6: a session file whose PID now belongs to another process, one that
// started at another time than the record says, is not a running session.
func TestReusedPIDIsNotLive(t *testing.T) {
	start := realProcStart(t)
	pid := os.Getpid()
	if got := sessionPIDs(t, map[string]any{"pid": pid, "procStart": "1"}); len(got) != 0 {
		t.Fatalf("a record of another process start counted as running: %v", got)
	}
	if got := sessionPIDs(t, map[string]any{"pid": pid, "procStart": start}); len(got) != 1 {
		t.Fatalf("the record of this very process was not running: %v", got)
	}
}

// Guard: a record without procStart is judged by the PID alone, as before.
func TestRecordWithoutProcStartFallsBack(t *testing.T) {
	if got := sessionPIDs(t, map[string]any{"pid": os.Getpid()}); len(got) != 1 {
		t.Fatalf("a record without procStart was not running: %v", got)
	}
}

// fakeProc points procRoot at a fake /proc whose namespace is pid:[7] and
// where pid's stat has start time 200 behind a command name with ") ".
func fakeProc(t *testing.T, pid int) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "self", "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pid:[7]", filepath.Join(root, "self", "ns", "pid")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	fields := append([]string{"S"}, strings.Fields(strings.Repeat("0 ", 18))...)
	fields = append(fields, "200", "0", "0")
	if err := os.MkdirAll(filepath.Join(root, strconv.Itoa(pid)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, filepath.Join(root, strconv.Itoa(pid), "stat"), strconv.Itoa(pid)+" (we) ird) "+strings.Join(fields, " ")+"\n")
	prev := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = prev })
}

func TestRecordAliveComparesProcStart(t *testing.T) {
	pid := os.Getpid()
	fakeProc(t, pid)
	for _, tc := range []struct {
		name      string
		procStart any
		domain    string
		alive     bool
	}{
		{"reused", "100", "", false},
		{"same start", "200", "", true},
		{"number", json.Number("200"), "", true},
		{"float", 100.0, "", false},
		{"this namespace", "100", "linux:x:pid:[7]", false},
		{"other namespace", "100", "linux:x:pid:[8]", true},
		{"unreadable start", "abc", "", true},
	} {
		if got := RecordAlive(pid, tc.procStart, tc.domain); got != tc.alive {
			t.Errorf("%s: RecordAlive = %v, want %v", tc.name, got, tc.alive)
		}
	}
	if RecordAlive(1, "200", "") {
		t.Error("PID 1 counted as a session")
	}
}
