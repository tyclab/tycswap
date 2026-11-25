package release

import (
	"os"
	"strings"
	"testing"
)

// notesWithMarkup is a version section the way entries are written: nested
// lists, code spans, links, a fenced block whose lines look like headings.
const notesWithMarkup = "- **Fixed:** `tycswap upgrade` on macOS waits for its build ([#150](https://github.com/tyclab/tycswap/pull/150)).\n" +
	"  - nested: `SHA256SUMS` lists it first\n" +
	"    1. deeper, with a [link](https://example.org/a_b) and `code ## v9.9.9`\n" +
	"\n" +
	"```text\n" +
	"## v8.8.8\n" +
	"## Unreleased\n" +
	"```\n" +
	"- Second entry."

func TestParseSectionsAndNotes(t *testing.T) {
	text := "# Changelog\n\nIntro line.\n\n## Unreleased\n\n- pending\n\n## v0.24.0\n\n" + notesWithMarkup +
		"\n\n\n## v0.23.3 and earlier\n\n- old\n"
	c, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []Kind
	for _, s := range c.Sections {
		kinds = append(kinds, s.Kind)
	}
	if len(kinds) != 3 || kinds[0] != Unreleased || kinds[1] != Version || kinds[2] != Legacy {
		t.Fatalf("kinds %v", kinds)
	}
	notes, ok := c.Notes("v0.24.0")
	if !ok || notes != notesWithMarkup {
		t.Errorf("notes = %q, want %q", notes, notesWithMarkup)
	}
	if _, ok := c.Notes("v8.8.8"); ok {
		t.Error("a heading inside a fenced block counted as a section")
	}
	if s := c.Sections[1]; s.Version != "v0.24.0" || s.Line != 9 {
		t.Errorf("section %+v", s)
	}
}

func TestParseCRLF(t *testing.T) {
	c, err := Parse("# Changelog\r\n\r\n## v1.0.0\r\n\r\n- one\r\n- two\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if notes, _ := c.Notes("v1.0.0"); notes != "- one\n- two" {
		t.Errorf("notes %q", notes)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"short version", "## v0.24\n- x\n", `"## v0.24" is not a section heading`},
		{"no v", "## 0.24.0\n- x\n", `"## 0.24.0" is not a section heading`},
		{"pre-release", "## v0.24.0-rc.1\n- x\n", "is not a section heading"},
		{"leading zero", "## v0.024.0\n- x\n", "is not a section heading"},
		{"date after version", "## v0.24.0 (2026-11-25)\n- x\n", "is not a section heading"},
		{"other words", "## Added\n- x\n", "is not a section heading"},
		{"empty heading", "##\n- x\n", "is not a section heading"},
		{"two unreleased", "## Unreleased\n## Unreleased\n", "names ## Unreleased twice"},
		{"unreleased below a version", "## v1.0.0\n- x\n## Unreleased\n", "## Unreleased must be the first section"},
		{"duplicate version", "## v1.0.0\n- x\n## v1.0.0\n- y\n", "names ## v1.0.0 twice"},
		{"ascending", "## v1.0.0\n- x\n## v1.1.0\n- y\n", "## v1.1.0 stands below ## v1.0.0"},
		{"legacy not last", LegacyHeading + "\n- x\n## v1.0.0\n- y\n", LegacyHeading + " must be the last section"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse error %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// The CHANGELOG at v0.23.3 (one Unreleased section holding every entry)
// still parses: it is the base of the change that introduced versions.
func TestParseTheFormatBeforeVersions(t *testing.T) {
	c, err := Parse("# Changelog\n\n## Unreleased\n\n- **Fixed:** one.\n- two.\n\n- three.\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sections) != 1 || c.Sections[0].Kind != Unreleased {
		t.Errorf("sections %+v", c.Sections)
	}
}

// The repository's own CHANGELOG.md parses: a heading CI's check would
// refuse fails here first.
func TestTheRepositoryChangelogParses(t *testing.T) {
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(string(b)); err != nil {
		t.Fatal(err)
	}
}
