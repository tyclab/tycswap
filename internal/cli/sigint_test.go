package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestSigintRoutingDefault: in plain (non-JSON) mode the cancel note goes to
// stdout — the behavior every non-env command relies on.
func TestSigintRoutingDefault(t *testing.T) {
	setSigintJSON(false)
	var out, errb bytes.Buffer
	writeSigintNote(ioStreams{out: &out, err: &errb})
	if !strings.Contains(out.String(), "Operation cancelled") {
		t.Errorf("plain-mode note missing from stdout: out=%q err=%q", out.String(), errb.String())
	}
	if errb.Len() != 0 {
		t.Errorf("plain-mode note leaked to stderr: %q", errb.String())
	}
}

// TestSigintRoutingJSON: JSON mode routes the note to stderr (spec 08§5),
// unchanged by the new env selector.
func TestSigintRoutingJSON(t *testing.T) {
	setSigintJSON(true)
	t.Cleanup(func() { setSigintJSON(false) })
	var out, errb bytes.Buffer
	writeSigintNote(ioStreams{out: &out, err: &errb})
	if out.Len() != 0 {
		t.Errorf("JSON-mode note leaked to stdout: %q", out.String())
	}
	if !strings.Contains(errb.String(), "Operation cancelled") {
		t.Errorf("JSON-mode note missing from stderr: %q", errb.String())
	}
}
