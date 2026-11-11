package autostart

// A provisioner may start the app from a scheduled task at logon, named like
// the Run value, instead of the Run value. Start at login has one owner per
// box (DESIGN A85): where that task exists it is read and switched, and a Run
// value beside it is removed; otherwise the Run value is, as before.

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// schtasksTimeout bounds one run: the tool answers in well under a second,
// and a hung one must not freeze the tray.
const schtasksTimeout = 10 * time.Second

// The exit codes of `/Query /HRESULT` that mean "no such task"; any other
// failure is an error, never read as no task.
const (
	hresultFileNotFound uint32 = 0x80070002
	hresultPathNotFound uint32 = 0x80070003
)

// errTaskInTests is the default runSchtasks' answer inside a test binary: a
// test never reads or changes a real scheduled task, except the CI test that
// installs execSchtasks for the task it created.
var errTaskInTests = errors.New("autostart: the real schtasks.exe is not reachable from tests; replace runSchtasks")

// runSchtasks runs schtasks.exe with args and returns what it printed
// (stdout, then stderr), its exit code, and an error when it did not run to
// its end. Tests replace it.
var runSchtasks = func(args ...string) ([]byte, uint32, error) {
	if testing.Testing() {
		return nil, 0, errTaskInTests
	}
	return execSchtasks(args...)
}

// The Run value's operations; tests replace them on every platform.
var (
	runValueSet    = registryEnable
	runValueRemove = registryDisable
	runValueExists = registryEnabled
)

// taskName is the task's path: the Run value's name in the root folder.
func taskName(c Config) string { return `\` + c.Label }

func windowsStatus(c Config) (Entry, error) {
	found, on, err := readTask(c)
	if err != nil {
		return Entry{}, err
	}
	if found {
		return Entry{Enabled: on, Task: c.Label}, nil
	}
	on, err = runValueExists(c)
	return Entry{Enabled: on}, err
}

func windowsSet(c Config, on bool) error {
	found, _, err := readTask(c)
	if err != nil {
		return err
	}
	if !found {
		if on {
			return runValueSet(c)
		}
		return runValueRemove(c)
	}
	flag := "/DISABLE"
	if on {
		flag = "/ENABLE"
	}
	if out, code, err := runSchtasks("/Change", "/TN", taskName(c), flag); err != nil || code != 0 {
		return taskError(c, "/Change", out, code, err)
	}
	// A Run value beside the task would start a second app at logon.
	return runValueRemove(c)
}

// readTask reads the task named like c's Run value; found is false only
// when the tool says there is no such task.
func readTask(c Config) (found, enabled bool, err error) {
	// /HRESULT makes the exit code the HRESULT: the one answer that tells
	// "no such task" from other failures, since the tool's text is translated.
	out, code, err := runSchtasks("/Query", "/TN", taskName(c), "/HRESULT", "/XML")
	switch {
	case err != nil:
		return false, false, taskError(c, "/Query", out, code, err)
	case code == hresultFileNotFound || code == hresultPathNotFound:
		return false, false, nil
	case code != 0:
		return false, false, taskError(c, "/Query", out, code, nil)
	}
	on, err := definitionEnabled(out)
	if err != nil {
		return true, false, fmt.Errorf("logon task %s: schtasks /Query: %w", taskName(c), err)
	}
	return true, on, nil
}

// taskError names the task and carries what the tool said.
func taskError(c Config, verb string, out []byte, code uint32, err error) error {
	msg := strings.TrimSpace(string(toolText(out)))
	if err != nil {
		if msg != "" {
			return fmt.Errorf("logon task %s: schtasks %s: %w: %s", taskName(c), verb, err, msg)
		}
		return fmt.Errorf("logon task %s: schtasks %s: %w", taskName(c), verb, err)
	}
	if msg == "" {
		return fmt.Errorf("logon task %s: schtasks %s exited %#x", taskName(c), verb, code)
	}
	return fmt.Errorf("logon task %s: schtasks %s (exit %#x): %s", taskName(c), verb, code, msg)
}

// definitionEnabled reads the task's own switch, Settings/Enabled, absent
// meaning enabled (the schema's default). Each trigger has an Enabled of its
// own, so no text match on the definition will do.
func definitionEnabled(out []byte) (bool, error) {
	var def struct {
		XMLName xml.Name `xml:"Task"`
		Enabled *string  `xml:"Settings>Enabled"`
	}
	text := toolText(out)
	d := xml.NewDecoder(bytes.NewReader(text))
	// The declaration says UTF-16 whatever the bytes are; toolText has
	// already made them UTF-8, so the declared encoding is ignored.
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&def); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(string(text)), "\n")
		return false, fmt.Errorf("no task definition (%v): %s", err, strings.TrimSpace(line))
	}
	if def.Enabled == nil {
		return true, nil
	}
	switch v := strings.TrimSpace(*def.Enabled); v {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("the task's Settings/Enabled is %q, not a boolean", v)
	}
}

// toolText makes the tool's output UTF-8. Written to a pipe it is UTF-16 on
// some systems and code-page bytes on others: a BOM or NUL bytes mean
// UTF-16, else the bytes are kept with what is not UTF-8 replaced (nothing
// read from them is outside ASCII).
func toolText(b []byte) []byte {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return utf16Text(b[2:], binary.LittleEndian)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return utf16Text(b[2:], binary.BigEndian)
	case bytes.IndexByte(b, 0) >= 0:
		// ASCII in UTF-16 without a BOM: the NUL comes first in big endian.
		if b[0] == 0 {
			return utf16Text(b, binary.BigEndian)
		}
		return utf16Text(b, binary.LittleEndian)
	}
	return bytes.ToValidUTF8(bytes.TrimPrefix(b, []byte("\uFEFF")), []byte("\uFFFD"))
}

func utf16Text(b []byte, order binary.ByteOrder) []byte {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = order.Uint16(b[2*i:])
	}
	return []byte(string(utf16.Decode(units)))
}
