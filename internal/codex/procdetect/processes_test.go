// processes_test.go — detecting running codex processes, so a switch can warn
// about restarts. Ports claude-swap PR #252 tests/test_codex_processes.py.
// Every test replaces ListProcesses or runCommand; none shells out.

package procdetect

import (
	"errors"
	"reflect"
	"testing"
)

// withLister swaps ListProcesses for the duration of the test.
func withLister(t *testing.T, fn func() ([]Proc, error)) {
	t.Helper()
	prev := ListProcesses
	ListProcesses = fn
	t.Cleanup(func() { ListProcesses = prev })
}

// withCommandOutput makes runCommand return stdout for any argv, recording
// the argv it was called with.
func withCommandOutput(t *testing.T, stdout string) *[]string {
	t.Helper()
	var got []string
	prev := runCommand
	runCommand = func(argv ...string) (string, error) {
		got = argv
		return stdout, nil
	}
	t.Cleanup(func() { runCommand = prev })
	return &got
}

func TestRunningCodexPIDs(t *testing.T) {
	cases := []struct {
		name string
		rows []Proc
		want []int
	}{
		{"no processes when the lister returns nothing", nil, []int{}},
		{"a codex process is detected", []Proc{{4242, "/Users/x/.local/bin/codex"}}, []int{4242}},
		{"an unrelated process is ignored", []Proc{{1, "/usr/bin/python"}}, []int{}},
		// `/Users/me/codex-notes/server` is not the codex CLI. Matching on the
		// executable name is what keeps the restart warning from crying wolf.
		{"a path merely containing codex does not match", []Proc{{7, "/Users/me/codex-notes/server"}}, []int{}},
		// codext switches seamlessly, but it is still a running Codex session.
		{"the codext fork counts as codex", []Proc{{9, "/usr/local/bin/codext"}}, []int{9}},
		{"a codex-prefixed binary does not match", []Proc{{3, "/usr/bin/codex-helper"}}, []int{}},
		{"listing order is kept", []Proc{{5, "codext"}, {2, "bash"}, {1, "codex"}}, []int{5, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withLister(t, func() ([]Proc, error) { return c.rows, nil })
			if got := RunningCodexPIDs(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("RunningCodexPIDs = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRunningCodexPIDs_ListerFailureDegradesToNoProcesses(t *testing.T) {
	withLister(t, func() ([]Proc, error) { return nil, errors.New("ps missing") })
	if got := RunningCodexPIDs(); got == nil || len(got) != 0 {
		t.Fatalf("RunningCodexPIDs = %#v, want empty non-nil", got)
	}
}

// "Never raises": a panicking lister is contained too.
func TestRunningCodexPIDs_ListerPanicDegradesToNoProcesses(t *testing.T) {
	withLister(t, func() ([]Proc, error) { panic("boom") })
	if got := RunningCodexPIDs(); got == nil || len(got) != 0 {
		t.Fatalf("RunningCodexPIDs = %#v, want empty non-nil", got)
	}
}

// The only coverage the POSIX branch gets — every other test patches the
// lister away.
func TestListProcesses_PosixPSParserReadsRealOutput(t *testing.T) {
	argv := withCommandOutput(t, "  501 codex\n  900 python3\n 1200 codext\n")
	rows, err := listProcesses(false)
	if err != nil {
		t.Fatal(err)
	}
	want := []Proc{{501, "codex"}, {900, "python3"}, {1200, "codext"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
	if !reflect.DeepEqual(*argv, []string{"ps", "-axo", "pid=,comm="}) {
		t.Fatalf("argv = %v", *argv)
	}
	withLister(t, func() ([]Proc, error) { return listProcesses(false) })
	if got := RunningCodexPIDs(); !reflect.DeepEqual(got, []int{501, 1200}) {
		t.Fatalf("RunningCodexPIDs = %v, want [501 1200]", got)
	}
}

// Windows support was an explicit decision, and this is the only coverage the
// win32 branch gets. The CSV is real `tasklist /FO CSV /NH` output.
func TestListProcesses_WindowsTasklistParserReadsRealCSVOutput(t *testing.T) {
	argv := withCommandOutput(t, "\"codex.exe\",\"4242\",\"Console\",\"1\",\"52,000 K\"\r\n"+
		"\"explorer.exe\",\"900\",\"Console\",\"1\",\"98,000 K\"\r\n")
	rows, err := listProcesses(true)
	if err != nil {
		t.Fatal(err)
	}
	want := []Proc{{4242, "codex.exe"}, {900, "explorer.exe"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
	if !reflect.DeepEqual(*argv, []string{"tasklist", "/FO", "CSV", "/NH"}) {
		t.Fatalf("argv = %v", *argv)
	}
	withLister(t, func() ([]Proc, error) { return listProcesses(true) })
	if got := RunningCodexPIDs(); !reflect.DeepEqual(got, []int{4242}) {
		t.Fatalf("RunningCodexPIDs = %v, want [4242]", got)
	}
}

func TestParsers_SkipMalformedRows(t *testing.T) {
	if got := parsePS("\n  PID COMMAND\n abc codex\n  12 /bin/my app\n"); !reflect.DeepEqual(got, []Proc{{12, "/bin/my app"}}) {
		t.Fatalf("parsePS = %v", got)
	}
	if got := parseTasklist("INFO: No tasks are running.\r\n\"x.exe\",\"n/a\"\r\n"); got != nil {
		t.Fatalf("parseTasklist = %v", got)
	}
}

func TestListProcesses_CommandFailureIsReturned(t *testing.T) {
	prev := runCommand
	runCommand = func(...string) (string, error) { return "", errors.New("exit 1") }
	t.Cleanup(func() { runCommand = prev })
	for _, windows := range []bool{false, true} {
		if _, err := listProcesses(windows); err == nil {
			t.Fatalf("listProcesses(windows=%v) swallowed the error", windows)
		}
	}
}

func TestExecutableName_StripsTheExeSuffix(t *testing.T) {
	cases := map[string]string{
		`C:\tools\codex.exe`: "codex",
		`C:\tools\CODEX.EXE`: "CODEX",
		"/usr/bin/codex":     "codex",
		"codex":              "codex",
		".exe":               "",
	}
	for in, want := range cases {
		if got := executableName(in); got != want {
			t.Errorf("executableName(%q) = %q, want %q", in, got, want)
		}
	}
}
