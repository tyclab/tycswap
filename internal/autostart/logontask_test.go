package autostart

import (
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// fakeSchtasks stands in for schtasks.exe: /Query prints query and exits
// queryExit (or fails to run with queryErr); /Change prints change and
// exits changeExit.
type fakeSchtasks struct {
	query      []byte
	queryExit  uint32
	queryErr   error
	change     []byte
	changeExit uint32
	calls      []string
}

func (f *fakeSchtasks) run(args ...string) ([]byte, uint32, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if len(args) > 0 && args[0] == "/Change" {
		return f.change, f.changeExit, nil
	}
	return f.query, f.queryExit, f.queryErr
}

// fakeRunKey stands in for the HKCU Run value.
type fakeRunKey struct {
	value bool
	calls []string
}

// useFakes installs f as schtasks.exe and returns the fake Run key.
func useFakes(t *testing.T, f *fakeSchtasks) *fakeRunKey {
	t.Helper()
	k := &fakeRunKey{}
	prevRun, prevSet, prevRemove, prevExists := runSchtasks, runValueSet, runValueRemove, runValueExists
	t.Cleanup(func() {
		runSchtasks, runValueSet, runValueRemove, runValueExists = prevRun, prevSet, prevRemove, prevExists
	})
	runSchtasks = f.run
	runValueSet = func(Config) error { k.calls = append(k.calls, "set"); k.value = true; return nil }
	runValueRemove = func(Config) error { k.calls = append(k.calls, "remove"); k.value = false; return nil }
	runValueExists = func(Config) (bool, error) { k.calls = append(k.calls, "exists"); return k.value, nil }
	return k
}

var winConfig = Config{Home: "/h", Exe: `C:\tycswap.exe`, GOOS: "windows"}

const noTaskMessage = "ERROR: The system cannot find the file specified.\r\n"

// taskDefinition is a definition as Register-ScheduledTask leaves it, with
// the logon trigger's Enabled ahead of the settings' own. settings "" leaves
// Settings/Enabled out.
func taskDefinition(trigger, settings string) string {
	own := ""
	if settings != "" {
		own = "\n    <Enabled>" + settings + "</Enabled>"
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Starts the tray at logon</Description>
    <URI>\io.github.tyclab.tycswap</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>` + trigger + `</Enabled>
      <UserId>EXAMPLE\me</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <StartWhenAvailable>true</StartWhenAvailable>
    <IdleSettings>
      <StopOnIdleEnd>true</StopOnIdleEnd>
    </IdleSettings>` + own + `
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>C:\Users\me\AppData\Local\Programs\tycswap\tycswap.exe</Command>
      <Arguments>app</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

func utf16Bytes(s string, order binary.ByteOrder, bom bool) []byte {
	units := utf16.Encode([]rune(s))
	if bom {
		units = append([]uint16{0xFEFF}, units...)
	}
	b := make([]byte, 2*len(units))
	for i, u := range units {
		order.PutUint16(b[2*i:], u)
	}
	return b
}

// TestLogonTaskEnabledIsTheSettingsSwitch: a task named like the Run value
// owns start at login, and its state is Settings/Enabled, absent meaning
// enabled; the trigger's own Enabled does not count. The definition the tool
// prints declares UTF-16 whether or not the bytes are.
func TestLogonTaskEnabledIsTheSettingsSwitch(t *testing.T) {
	plain := func(s string) []byte { return []byte(s) }
	cases := []struct {
		name string
		out  []byte
		want bool
	}{
		{"absent", plain(taskDefinition("true", "")), true},
		{"true", plain(taskDefinition("true", "true")), true},
		{"false", plain(taskDefinition("false", "false")), false},
		{"trigger off, task on", plain(taskDefinition("false", "true")), true},
		{"trigger on, task off", plain(taskDefinition("true", "false")), false},
		// A code-page byte (ü in CP850) in a description is no UTF-8.
		{"UTF-16 declared over code-page bytes", plain(strings.Replace(taskDefinition("true", "false"), "Starts", "Startet \x81ber", 1)), false},
		{"UTF-16LE with BOM", utf16Bytes(taskDefinition("true", "false"), binary.LittleEndian, true), false},
		{"UTF-16LE without BOM", utf16Bytes(taskDefinition("false", "true"), binary.LittleEndian, false), true},
		{"UTF-16BE with BOM", utf16Bytes(taskDefinition("true", "false"), binary.BigEndian, true), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSchtasks{query: tc.out}
			k := useFakes(t, f)
			k.value = !tc.want // a Run value that disagrees must not be read
			got, err := Status(winConfig)
			if err != nil {
				t.Fatal(err)
			}
			if want := (Entry{Enabled: tc.want, Task: Label}); got != want {
				t.Errorf("Status = %+v, want %+v", got, want)
			}
			if on, err := Enabled(winConfig); err != nil || on != tc.want {
				t.Errorf("Enabled = %v, %v; want %v", on, err, tc.want)
			}
			if want := `/Query /TN \io.github.tyclab.tycswap /HRESULT /XML`; len(f.calls) == 0 || f.calls[0] != want {
				t.Errorf("schtasks calls = %q, want %q first", f.calls, want)
			}
			if len(k.calls) != 0 {
				t.Errorf("the Run value was used beside a task: %v", k.calls)
			}
		})
	}
}

// TestNoLogonTaskFallsBackToTheRunValue: "no such task" (the HRESULT the
// tool exits with under /HRESULT) leaves start at login to the Run value.
func TestNoLogonTaskFallsBackToTheRunValue(t *testing.T) {
	f := &fakeSchtasks{query: []byte(noTaskMessage), queryExit: 0x80070002}
	k := useFakes(t, f)
	k.value = true
	remote := winConfig
	remote.Label = Label + ".remote"
	got, err := Status(remote)
	if err != nil || got != (Entry{Enabled: true}) {
		t.Fatalf("Status = %+v, %v; want the Run value's on, no task", got, err)
	}
	if want := `/Query /TN \io.github.tyclab.tycswap.remote /HRESULT /XML`; !slices.Equal(f.calls, []string{want}) {
		t.Errorf("schtasks calls = %q, want %q", f.calls, want)
	}
	if err := Disable(remote); err != nil || k.value {
		t.Errorf("Disable = %v, Run value %v; want it removed", err, k.value)
	}
	if err := Enable(remote); err != nil || !k.value {
		t.Errorf("Enable = %v, Run value %v; want it set", err, k.value)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "/Change") {
			t.Errorf("a missing task was changed: %q", c)
		}
	}
}

// TestUnreadableLogonTaskIsAnError: a task the tool cannot read, or a tool
// that does not answer, is an error naming the task with the tool's
// message, never "no task" (which would let the Run value become a second
// owner).
func TestUnreadableLogonTaskIsAnError(t *testing.T) {
	cases := []struct {
		name string
		f    fakeSchtasks
		want string
	}{
		{"access denied", fakeSchtasks{query: []byte("ERROR: Access is denied.\r\n"), queryExit: 0x80070005}, "ERROR: Access is denied."},
		{"no answer", fakeSchtasks{queryErr: errors.New("no answer within 10s")}, "no answer within 10s"},
		{"not a task definition", fakeSchtasks{query: []byte("INFO: nothing here\r\n")}, "INFO: nothing here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := useFakes(t, &tc.f)
			for name, call := range map[string]func() error{
				"Status":  func() error { _, err := Status(winConfig); return err },
				"Enabled": func() error { _, err := Enabled(winConfig); return err },
				"Enable":  func() error { return Enable(winConfig) },
				"Disable": func() error { return Disable(winConfig) },
			} {
				err := call()
				if err == nil || !strings.Contains(err.Error(), `\io.github.tyclab.tycswap`) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s = %v, want an error naming the task and carrying %q", name, err, tc.want)
				}
			}
			if len(k.calls) != 0 {
				t.Errorf("the Run value was used: %v", k.calls)
			}
		})
	}
}

// TestLogonTaskOwnsEnableAndDisable: with a task, on and off switch the
// task and leave no Run value beside it.
func TestLogonTaskOwnsEnableAndDisable(t *testing.T) {
	f := &fakeSchtasks{query: []byte(taskDefinition("true", "false"))}
	k := useFakes(t, f)
	k.value = true // an earlier hand registration
	if err := Enable(winConfig); err != nil {
		t.Fatal(err)
	}
	if want := `/Change /TN \io.github.tyclab.tycswap /ENABLE`; !slices.Contains(f.calls, want) {
		t.Errorf("schtasks calls = %q, want %q", f.calls, want)
	}
	if k.value {
		t.Error("Enable with a task kept the Run value: two owners")
	}

	f.calls, k.calls = nil, nil
	if err := Disable(winConfig); err != nil {
		t.Fatal(err)
	}
	if want := `/Change /TN \io.github.tyclab.tycswap /DISABLE`; !slices.Contains(f.calls, want) {
		t.Errorf("schtasks calls = %q, want %q", f.calls, want)
	}
	if k.value || slices.Contains(k.calls, "set") {
		t.Errorf("Disable with a task created a Run value: %v", k.calls)
	}
}

// TestFailingTaskChangeIsAnError: a /Change the tool refuses is an error
// naming the task with its message, and the Run value stays as it was.
func TestFailingTaskChangeIsAnError(t *testing.T) {
	f := &fakeSchtasks{query: []byte(taskDefinition("true", "true")), change: []byte("ERROR: Access is denied.\r\n"), changeExit: 1}
	k := useFakes(t, f)
	k.value = true
	for name, call := range map[string]func(Config) error{"Enable": Enable, "Disable": Disable} {
		err := call(winConfig)
		if err == nil || !strings.Contains(err.Error(), `\io.github.tyclab.tycswap`) || !strings.Contains(err.Error(), "ERROR: Access is denied.") {
			t.Errorf("%s = %v, want an error naming the task and carrying the tool's message", name, err)
		}
	}
	if !k.value || len(k.calls) != 0 {
		t.Errorf("the Run value was touched after a failed change: %v", k.calls)
	}
}

// TestStatusElsewhereHasNoTask: macOS and Linux keep their own entry.
func TestStatusElsewhereHasNoTask(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	f := &fakeSchtasks{}
	useFakes(t, f)
	for _, goos := range []string{"darwin", "linux"} {
		c := Config{Home: t.TempDir(), Exe: "/x", GOOS: goos, Run: (&recorder{}).run}
		if err := Enable(c); err != nil {
			t.Fatal(err)
		}
		if got, err := Status(c); err != nil || got != (Entry{Enabled: true}) {
			t.Errorf("%s: Status = %+v, %v", goos, got, err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("schtasks ran off Windows: %q", f.calls)
	}
}

// TestSchtasksRefusesInTests: the default runSchtasks is the real tool,
// which in a test binary refuses before starting anything.
func TestSchtasksRefusesInTests(t *testing.T) {
	if _, _, err := runSchtasks("/Query", "/TN", `\`+Label, "/HRESULT", "/XML"); !errors.Is(err, errTaskInTests) {
		t.Fatalf("runSchtasks = %v, want errTaskInTests", err)
	}
	k := &fakeRunKey{}
	prevExists := runValueExists
	t.Cleanup(func() { runValueExists = prevExists })
	runValueExists = func(Config) (bool, error) { k.calls = append(k.calls, "exists"); return true, nil }
	if _, err := Enabled(winConfig); !errors.Is(err, errTaskInTests) || len(k.calls) != 0 {
		t.Errorf("Enabled = %v after %v, want errTaskInTests and no Run value read", err, k.calls)
	}
}
