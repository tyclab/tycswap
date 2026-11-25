// Package release decides and publishes tycswap's releases from CHANGELOG.md
// (DESIGN A119, A121). The pull request check, the release workflow and the
// runner retry all run this code; the workflows only call it.
package release

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// TopHeading is the one section new entries go into, at its top; nobody
// renames it. UnreleasedHeading is where entries went before A121, and
// LegacyHeading holds every entry written before versions had sections.
const (
	TopHeading        = "## Since v0.24.1"
	UnreleasedHeading = "## Unreleased"
	LegacyHeading     = "## v0.23.3 and earlier"
)

// Kind is what a level-two heading of CHANGELOG.md is.
type Kind int

const (
	Top Kind = iota
	Unreleased
	Version
	Legacy
)

// Section is one level-two heading and the text below it, up to the next.
type Section struct {
	Kind    Kind
	Version string // vMAJOR.MINOR.PATCH of a Version section
	Heading string
	// Body is the text under the heading without its surrounding blank lines.
	Body  string
	Line  int
	lines []string // the body as written, from the line after the heading
}

// Changelog is CHANGELOG.md's sections in file order.
type Changelog struct {
	Sections []Section
	preamble []string // the lines above the first heading, from line 1
}

var versionHeading = regexp.MustCompile(`^## (v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))$`)

// Parse reads CHANGELOG.md. Every level-two heading outside a fenced block
// is the top section or ## Unreleased (first), ## vMAJOR.MINOR.PATCH (newest
// first, each once) or the legacy heading (last); anything else is an error.
func Parse(text string) (Changelog, error) {
	var c Changelog
	var problems []error
	var cur *Section
	var body []string
	flush := func() {
		if cur != nil {
			cur.Body, cur.lines = trimBlankLines(body), body
			c.Sections = append(c.Sections, *cur)
		} else if c.Sections == nil {
			c.preamble = body
		}
	}
	fence := ""
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		fence = nextFence(fence, line)
		heading := strings.TrimRight(line, " \t")
		if fence != "" || (heading != "##" && !strings.HasPrefix(heading, "## ")) {
			body = append(body, line)
			continue
		}
		flush()
		cur, body = &Section{Heading: heading, Line: i + 1}, nil
		switch m := versionHeading.FindStringSubmatch(heading); {
		case heading == TopHeading:
			cur.Kind = Top
		case heading == UnreleasedHeading:
			cur.Kind = Unreleased
		case heading == LegacyHeading:
			cur.Kind = Legacy
		case m != nil:
			cur.Kind, cur.Version = Version, m[1]
		default:
			problems = append(problems, fmt.Errorf("line %d: %q is not a section heading CHANGELOG.md allows (%s, ## vMAJOR.MINOR.PATCH, %s)",
				i+1, heading, TopHeading, LegacyHeading))
			cur = nil
		}
	}
	flush()
	problems = append(problems, c.order()...)
	return c, errors.Join(problems...)
}

func (c Changelog) order() []error {
	var problems []error
	seen := map[string]bool{}
	_, hasTop := c.section(Top)
	var prev *Section
	for i := range c.Sections {
		s := &c.Sections[i]
		if seen[s.Heading] {
			problems = append(problems, fmt.Errorf("line %d: CHANGELOG.md names %s twice", s.Line, s.Heading))
			continue
		}
		seen[s.Heading] = true
		switch {
		case (s.Kind == Top || (s.Kind == Unreleased && !hasTop)) && i > 0:
			problems = append(problems, fmt.Errorf("line %d: %s must be the first section", s.Line, s.Heading))
		case s.Kind == Legacy && i < len(c.Sections)-1:
			problems = append(problems, fmt.Errorf("line %d: %s must be the last section", s.Line, LegacyHeading))
		case s.Kind == Version && prev != nil && semver.Compare(s.Version, prev.Version) >= 0:
			problems = append(problems, fmt.Errorf("line %d: %s stands below %s: versions go newest first", s.Line, s.Heading, prev.Heading))
		}
		if s.Kind == Version {
			prev = s
		}
	}
	return problems
}

// section is the first section of kind.
func (c Changelog) section(kind Kind) (Section, bool) {
	for _, s := range c.Sections {
		if s.Kind == kind {
			return s, true
		}
	}
	return Section{}, false
}

// top is the section new entries go into: the top section, or ## Unreleased
// in a CHANGELOG.md from before A121.
func (c Changelog) top() (Section, bool) {
	if s, ok := c.section(Top); ok {
		return s, true
	}
	return c.section(Unreleased)
}

// Entry is one top-level list item: its first line starts with "- ", and the
// lines up to the next such line or heading continue it.
type Entry struct {
	Text string // as written, without trailing blank lines: the release notes carry it
	Line int    // its first line in the file
}

// key is the entry's identity: its text without trailing white space.
func (e Entry) key() string {
	lines := strings.Split(e.Text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// Fixed says the entry is a fix: it starts with **Fixed:**.
func (e Entry) Fixed() bool { return strings.HasPrefix(e.Text, "- **Fixed:**") }

// Entries are the entries of every section, in file order.
func (c Changelog) Entries() []Entry {
	var out []Entry
	for _, s := range c.Sections {
		out = append(out, s.Entries()...)
	}
	return out
}

// Entries are the section's entries, in file order.
func (s Section) Entries() []Entry {
	entries, _ := splitEntries(s.lines, s.Line+1)
	return entries
}

// splitEntries reads lines, the first of which is line number first, as
// entries. stray is each line outside a fenced block that starts no entry
// and continues none: text above the first entry, or a list item marked
// with * or +.
func splitEntries(lines []string, first int) (entries []Entry, stray []int) {
	var cur []string
	at := 0
	flush := func() {
		if cur != nil {
			entries = append(entries, Entry{Text: trimBlankLines(cur), Line: at})
		}
	}
	fence := ""
	for i, line := range lines {
		wasFence := fence
		fence = nextFence(fence, line)
		switch {
		case wasFence == "" && strings.HasPrefix(line, "- "):
			flush()
			cur, at = []string{line}, first+i
		case wasFence == "" && (strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ")):
			stray = append(stray, first+i)
		case cur != nil:
			cur = append(cur, line)
		case strings.TrimSpace(line) != "":
			stray = append(stray, first+i)
		}
	}
	flush()
	return entries, stray
}

// nextFence is the fence open after line, given the one open before it: a
// backtick or tilde run of three or more (at most three spaces of indent)
// opens a fenced block, and a run of the same character at least as long
// closes it.
func nextFence(open, line string) string {
	run := fenceRun(line)
	switch {
	case run == "":
		return open
	case open == "":
		return run
	case run[0] == open[0] && len(run) >= len(open):
		return ""
	}
	return open
}

func fenceRun(line string) string {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return ""
	}
	for _, ch := range []string{"`", "~"} {
		if n := len(t) - len(strings.TrimLeft(t, ch)); n >= 3 {
			return strings.Repeat(ch, n)
		}
	}
	return ""
}

func trimBlankLines(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
