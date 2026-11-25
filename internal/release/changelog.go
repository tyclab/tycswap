// Package release decides and publishes tycswap's releases from CHANGELOG.md
// (DESIGN A119). The pull request check, the release workflow and the
// runner retry all run this code; the workflows only call it.
package release

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// UnreleasedHeading collects entries no version names yet; LegacyHeading
// holds every entry written before versions had sections, as they were.
const (
	UnreleasedHeading = "## Unreleased"
	LegacyHeading     = "## v0.23.3 and earlier"
)

// Kind is what a level-two heading of CHANGELOG.md is.
type Kind int

const (
	Unreleased Kind = iota
	Version
	Legacy
)

// Section is one level-two heading and the text below it, up to the next.
type Section struct {
	Kind    Kind
	Version string // vMAJOR.MINOR.PATCH of a Version section
	Heading string
	// Body is the text under the heading without its surrounding blank
	// lines: a version's release notes.
	Body string
	Line int
}

// Changelog is CHANGELOG.md's sections in file order.
type Changelog struct{ Sections []Section }

var versionHeading = regexp.MustCompile(`^## (v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))$`)

// Parse reads CHANGELOG.md. Every level-two heading outside a fenced block
// is ## Unreleased (first, at most once), ## vMAJOR.MINOR.PATCH (newest
// first, each once) or the legacy heading (last); anything else is an error.
func Parse(text string) (Changelog, error) {
	var c Changelog
	var problems []error
	var cur *Section
	var body []string
	flush := func() {
		if cur != nil {
			cur.Body = trimBlankLines(body)
			c.Sections = append(c.Sections, *cur)
		}
	}
	fence := ""
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if run := fenceRun(line); run != "" {
			switch {
			case fence == "":
				fence = run
			case run[0] == fence[0] && len(run) >= len(fence):
				fence = ""
			}
		}
		heading := strings.TrimRight(line, " \t")
		if fence != "" || (heading != "##" && !strings.HasPrefix(heading, "## ")) {
			body = append(body, line)
			continue
		}
		flush()
		cur, body = &Section{Heading: heading, Line: i + 1}, nil
		switch m := versionHeading.FindStringSubmatch(heading); {
		case heading == UnreleasedHeading:
			cur.Kind = Unreleased
		case heading == LegacyHeading:
			cur.Kind = Legacy
		case m != nil:
			cur.Kind, cur.Version = Version, m[1]
		default:
			problems = append(problems, fmt.Errorf("line %d: %q is not a section heading CHANGELOG.md allows (%s, ## vMAJOR.MINOR.PATCH, %s)",
				i+1, heading, UnreleasedHeading, LegacyHeading))
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
	var prev *Section
	for i := range c.Sections {
		s := &c.Sections[i]
		if seen[s.Heading] {
			problems = append(problems, fmt.Errorf("line %d: CHANGELOG.md names %s twice", s.Line, s.Heading))
			continue
		}
		seen[s.Heading] = true
		switch {
		case s.Kind == Unreleased && i > 0:
			problems = append(problems, fmt.Errorf("line %d: %s must be the first section", s.Line, UnreleasedHeading))
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

// Notes is version's section body, and whether CHANGELOG.md has the section.
func (c Changelog) Notes(version string) (string, bool) {
	for _, s := range c.Sections {
		if s.Kind == Version && s.Version == version {
			return s.Body, true
		}
	}
	return "", false
}

// fenceRun is the backtick or tilde run that opens or closes a fenced code
// block on line (at most three spaces of indent), "" when there is none.
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
