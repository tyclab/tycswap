package release

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/mod/semver"
)

// Pending is what a merge releases: the entries its CHANGELOG.md has that
// the newest tag before it lacks, and the version they get.
type Pending struct {
	Since   string // the newest v* tag reachable from the merge's first parent, "" for none
	Version string // "" when there is no new entry
	Bump    string // "patch" or "minor"
	Entries []Entry
}

// Notes are the new entries in file order: the release's notes.
func (p Pending) Notes() string {
	texts := make([]string, len(p.Entries))
	for i, e := range p.Entries {
		texts[i] = e.Text
	}
	return strings.Join(texts, "\n")
}

// PendingOver is what head releases over the CHANGELOG.md of tag since
// (tagged, "" when there is none): the entries of head that tagged lacks.
func PendingOver(head, tagged Changelog, since string) Pending {
	p := Pending{Since: since, Entries: NewEntries(head, tagged)}
	if len(p.Entries) > 0 {
		p.Version, p.Bump = NextVersion(since, p.Entries)
	}
	return p
}

// NewEntries are the entries of head that tagged does not have, in head's
// file order. Entries are told apart by their text, counted: of two equal
// entries the lower, older one is the one tagged has.
func NewEntries(head, tagged Changelog) []Entry {
	have := map[string]int{}
	for _, e := range tagged.Entries() {
		have[e.key()]++
	}
	all := head.Entries()
	var fresh []Entry
	for i := len(all) - 1; i >= 0; i-- {
		if k := all[i].key(); have[k] > 0 {
			have[k]--
			continue
		}
		fresh = append([]Entry{all[i]}, fresh...)
	}
	return fresh
}

// NextVersion is since with its patch number raised when every entry is a
// fix, else its minor number; "" counts as v0.0.0. A major release is never
// computed.
func NextVersion(since string, entries []Entry) (version, bump string) {
	major, minor, patch := 0, 0, 0
	if f := strings.Split(strings.TrimPrefix(semver.Canonical(since), "v"), "."); len(f) == 3 {
		major, _ = strconv.Atoi(f[0])
		minor, _ = strconv.Atoi(f[1])
		patch, _ = strconv.Atoi(strings.SplitN(f[2], "-", 2)[0])
	}
	bump = "patch"
	for _, e := range entries {
		if !e.Fixed() {
			bump = "minor"
		}
	}
	if bump == "patch" {
		patch++
	} else {
		minor, patch = minor+1, 0
	}
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch), bump
}

// Newest is the greatest version among tags, "" when none is one.
func Newest(tags []string) string {
	newest := ""
	for _, t := range tags {
		if semver.IsValid(t) && (newest == "" || semver.Compare(t, newest) > 0) {
			newest = t
		}
	}
	return newest
}

// CheckChange is the pull request check. head is CHANGELOG.md at the
// change, base at what it merges into, tagged at since, the newest v* tag
// reachable from base. It refuses a malformed head, a heading that is no
// longer written (## Unreleased, a version), an entry outside the top
// section, a change to the history sections and a change to or removal of
// an entry base has; otherwise it returns what merging the change releases.
func CheckChange(head, base, tagged, since string) (Pending, error) {
	h, err := Parse(head)
	if err != nil {
		return Pending{}, fmt.Errorf("CHANGELOG.md: %w", err)
	}
	b, err := Parse(base)
	if err != nil {
		return Pending{}, fmt.Errorf("the base's CHANGELOG.md: %w", err)
	}
	t, err := Parse(tagged)
	if err != nil {
		return Pending{}, fmt.Errorf("CHANGELOG.md at %s: %w", since, err)
	}
	problems := h.layoutProblems()
	problems = append(problems, historyProblems(h, b)...)
	problems = append(problems, keptProblems(h, b, t, since)...)
	if len(problems) > 0 {
		return Pending{}, errors.Join(problems...)
	}
	return PendingOver(h, t, since), nil
}

// layoutProblems: the top section is first, entries stand in it only, and
// it holds nothing but entries.
func (c Changelog) layoutProblems() []error {
	var problems []error
	top, ok := c.section(Top)
	_, hasUnreleased := c.section(Unreleased)
	if !ok && !hasUnreleased {
		problems = append(problems, fmt.Errorf("CHANGELOG.md has no %s: it is the first section, and a pull request writes its entry at its top", TopHeading))
	}
	// The title and any other text above the sections are free.
	preamble, _ := splitEntries(c.preamble, 1)
	for _, e := range preamble {
		problems = append(problems, fmt.Errorf("line %d: an entry above %s (%s): move it to the top of %s", e.Line, TopHeading, excerpt(e), TopHeading))
	}
	for _, s := range c.Sections {
		if s.Kind == Unreleased {
			problems = append(problems, fmt.Errorf("line %d: %s is gone (DESIGN A121): move its entries to the top of %s and delete the heading; a merge releases every entry it adds, with a computed version", s.Line, UnreleasedHeading, TopHeading))
		}
	}
	if ok {
		_, stray := splitEntries(top.lines, top.Line+1)
		for _, n := range stray {
			problems = append(problems, fmt.Errorf("line %d: text in %s that is no entry: each entry starts with \"- \" at the start of a line, and its other lines continue it", n, TopHeading))
		}
	}
	return problems
}

// historyProblems: the sections below the top one are as base has them,
// and head adds none.
func historyProblems(h, b Changelog) []error {
	var problems []error
	inHead := map[string]Section{}
	for _, s := range h.Sections {
		inHead[s.Heading] = s
	}
	inBase := map[string]bool{}
	for _, s := range b.Sections {
		inBase[s.Heading] = true
		if s.Kind == Top || s.Kind == Unreleased {
			continue
		}
		switch hs, ok := inHead[s.Heading]; {
		case !ok:
			problems = append(problems, fmt.Errorf("removes %s, which main has: released history stays as it is", s.Heading))
		case hs.Body != s.Body:
			problems = append(problems, fmt.Errorf("line %d: changes %s, which main has: released history stays as it is; correct an entry with a new one at the top of %s", hs.Line, s.Heading, TopHeading))
		}
	}
	for _, s := range h.Sections {
		if inBase[s.Heading] || s.Kind == Top || s.Kind == Unreleased {
			continue
		}
		if s.Kind == Version {
			problems = append(problems, fmt.Errorf("line %d: %s names a version, which a merge no longer does (DESIGN A121): move its entries to the top of %s and delete the heading; the release's version is computed", s.Line, s.Heading, TopHeading))
		} else {
			problems = append(problems, fmt.Errorf("line %d: %s is a section main does not have: move its entries to the top of %s and delete the heading", s.Line, s.Heading, TopHeading))
		}
	}
	return problems
}

// keptProblems: every entry of base's top section is in head's top section,
// word for word.
func keptProblems(h, b, t Changelog, since string) []error {
	var problems []error
	headTop, ok := h.section(Top)
	if !ok {
		return nil // layoutProblems says where the entries go
	}
	have := map[string]int{}
	for _, e := range headTop.Entries() {
		have[e.key()]++
	}
	released := map[string]int{}
	for _, e := range t.Entries() {
		released[e.key()]++
	}
	baseTop, _ := b.top()
	for _, e := range baseTop.Entries() {
		k := e.key()
		if have[k] > 0 {
			have[k]--
			continue
		}
		if released[k] > 0 {
			problems = append(problems, fmt.Errorf("changes or removes the entry released in %s at line %d of main's CHANGELOG.md (%s): a released entry stays as it is; correct it with a new entry at the top of %s", since, e.Line, excerpt(e), TopHeading))
		} else {
			problems = append(problems, fmt.Errorf("changes or removes the entry at line %d of main's CHANGELOG.md (%s), which main's next release carries: a merged entry stays as it is; correct it with a new entry at the top of %s", e.Line, excerpt(e), TopHeading))
		}
	}
	return problems
}

// excerpt is the start of an entry's first line, for a message.
func excerpt(e Entry) string {
	line, _, _ := strings.Cut(e.Text, "\n")
	if utf8.RuneCountInString(line) > 60 {
		line = string([]rune(line)[:60]) + "…"
	}
	return fmt.Sprintf("%q", line)
}
