package release

import (
	"errors"
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

func TestParseSectionsAndEntries(t *testing.T) {
	text := "# Changelog\n\nIntro line.\n\n" + TopHeading + "\n\n- pending\n\n## v0.24.0\n\n" + notesWithMarkup +
		"\n\n\n## v0.23.3 and earlier\n\n- old\n"
	c, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []Kind
	for _, s := range c.Sections {
		kinds = append(kinds, s.Kind)
	}
	if len(kinds) != 3 || kinds[0] != Top || kinds[1] != Version || kinds[2] != Legacy {
		t.Fatalf("kinds %v", kinds)
	}
	if body := c.Sections[1].Body; body != notesWithMarkup {
		t.Errorf("body = %q, want %q", body, notesWithMarkup)
	}
	for _, s := range c.Sections {
		if s.Heading == "## v8.8.8" {
			t.Error("a heading inside a fenced block counted as a section")
		}
	}
	if s := c.Sections[1]; s.Version != "v0.24.0" || s.Line != 9 {
		t.Errorf("section %+v", s)
	}
	// The fenced block, a heading-like line in it and the nested list belong
	// to the first entry; the second starts at its own "- ".
	entries := c.Sections[1].Entries()
	if len(entries) != 2 || !strings.HasSuffix(entries[0].Text, "```") || entries[1].Text != "- Second entry." || entries[1].Line != 19 {
		t.Errorf("entries %+v", entries)
	}
	if !entries[0].Fixed() || entries[1].Fixed() {
		t.Error("Fixed")
	}
	if all := c.Entries(); len(all) != 4 || all[0].Text != "- pending" || all[3].Text != "- old" {
		t.Errorf("all entries %+v", all)
	}
}

func TestParseCRLF(t *testing.T) {
	c, err := Parse("# Changelog\r\n\r\n## v1.0.0\r\n\r\n- one\r\n- two\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if body := c.Sections[0].Body; body != "- one\n- two" {
		t.Errorf("body %q", body)
	}
}

// An entry is a top-level item: "- " at the start of a line outside a
// fenced block, and every line up to the next one; text before the first
// entry and items marked * or + are stray.
func TestEntries(t *testing.T) {
	lines := strings.Split("intro\n\n- **Fixed:** one\n  continued\n\n```\n- not an entry\n```\n- two\n* starred\n+ plus\n- three  \n\n", "\n")
	entries, stray := splitEntries(lines, 10)
	var texts []string
	for _, e := range entries {
		texts = append(texts, e.Text)
	}
	want := []string{"- **Fixed:** one\n  continued\n\n```\n- not an entry\n```", "- two", "- three  "}
	if strings.Join(texts, "|") != strings.Join(want, "|") || entries[1].Line != 18 {
		t.Errorf("entries %q at %+v", texts, entries)
	}
	if len(stray) != 3 || stray[0] != 10 || stray[1] != 19 || stray[2] != 20 {
		t.Errorf("stray %v", stray)
	}
	if entries[2].key() != "- three" {
		t.Errorf("key %q", entries[2].key())
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
		{"two top sections", TopHeading + "\n" + TopHeading + "\n", "names " + TopHeading + " twice"},
		{"top below a version", "## v1.0.0\n- x\n" + TopHeading + "\n", TopHeading + " must be the first section"},
		{"a since heading for another version", "## Since v0.25.0\n- x\n", `"## Since v0.25.0" is not a section heading`},
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

// The repository's own CHANGELOG.md parses and has the layout the check
// wants: a heading or stray line CI's check would refuse fails here first.
func TestTheRepositoryChangelogParses(t *testing.T) {
	c, err := Parse(repositoryChangelog(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(c.layoutProblems()...); err != nil {
		t.Fatal(err)
	}
}

func repositoryChangelog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout has CRLF line ends; the tests edit the text by line.
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}
