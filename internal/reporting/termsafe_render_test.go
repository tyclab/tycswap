package reporting

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/usage"
)

// TestRenderAccountsStripsControlSequences: an org name, alias or email
// carrying an escape sequence prints as plain text.
func TestRenderAccountsStripsControlSequences(t *testing.T) {
	s := newStore(t, nil, nil)
	writeSequenceRaw(t, s, `{"sequence": [1], "accounts": {"1": {"email": "a@example.com", "organizationUuid": ""}}}`)
	infos := []AccountInfo{{Number: 1, Email: "a@example.com\x1b]0;x\x07", OrgName: "Evil\x1b[2J\u009b31m", Alias: "w\x1b[1m"}}
	var buf bytes.Buffer
	renderAccounts(&buf, s, infos, map[string]usage.UsageEntry{}, false)
	out := buf.String()
	for _, bad := range []string{"\x1b]0", "\x1b[2J", "\u009b", "\x07", "\x1b[1m"} {
		if strings.Contains(out, bad) {
			t.Errorf("output carries %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "Evil[2J31m") {
		t.Errorf("org name not shown as text: %q", out)
	}
}

// TestDuplicateWarningKeepsControlCharactersInJSONTextStripsThem: the
// duplicate-account warning carries the stored email into the --json payload
// unchanged; the human list prints it without the control sequence.
func TestDuplicateWarningKeepsControlCharactersInJSONTextStripsThem(t *testing.T) {
	s := newStore(t, nil, nil)
	writeSequenceRaw(t, s, `{"sequence": [1, 2], "accounts": {
		"1": {"email": "a@example.com", "uuid": "u-1", "organizationUuid": ""},
		"2": {"email": "a@example.com", "uuid": "u-1", "organizationUuid": ""}}}`)
	const email = "a@example.com\x1b[2J"
	infos := []AccountInfo{{Number: 1, Email: email}, {Number: 2, Email: email}}
	entries := map[string]usage.UsageEntry{}

	payload := buildListPayload(s, infos, entries, false)
	dup, _ := payload["duplicateAccountWarnings"].([]string)
	if len(dup) != 1 || !strings.Contains(dup[0], email) {
		t.Fatalf("duplicateAccountWarnings = %q, want one warning with the stored email", dup)
	}

	var buf bytes.Buffer
	renderAccounts(&buf, s, infos, entries, false)
	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Errorf("text output carries an escape: %q", out)
	}
	if !strings.Contains(out, "both authenticate as a@example.com[2J") {
		t.Errorf("warning not shown as plain text: %q", out)
	}
}
