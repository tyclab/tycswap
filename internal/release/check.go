package release

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

// CheckVersion is the one rule the pull request check and the release step
// share. head is CHANGELOG.md at the change, base at what it merges into,
// tags the v* tags a new version must exceed. It returns the version head
// names that base does not ("" for none) and refuses: a heading that is not
// vMAJOR.MINOR.PATCH, two new versions, an empty one, one not greater than
// every tag and every version base names, a repeat of a tag, and a change to
// or removal of any section base has besides Unreleased.
func CheckVersion(head, base string, tags []string) (string, error) {
	h, err := Parse(head)
	if err != nil {
		return "", fmt.Errorf("CHANGELOG.md: %w", err)
	}
	b, err := Parse(base)
	if err != nil {
		return "", fmt.Errorf("the base's CHANGELOG.md: %w", err)
	}
	var problems []error
	inHead := map[string]Section{}
	for _, s := range h.Sections {
		inHead[s.Heading] = s
	}
	highest, known := "", map[string]bool{}
	for _, s := range b.Sections {
		if s.Kind == Unreleased {
			continue
		}
		hs, ok := inHead[s.Heading]
		switch {
		case !ok:
			problems = append(problems, fmt.Errorf("removes %s, which the base already has", s.Heading))
		case hs.Body != s.Body:
			problems = append(problems, fmt.Errorf("changes %s, which the base already has: a version's notes stay as they were released", s.Heading))
		}
		if s.Kind == Version {
			known[s.Version] = true
			highest = maxVersion(highest, s.Version)
		}
	}
	for _, t := range tags {
		if semver.IsValid(t) {
			highest = maxVersion(highest, t)
		}
	}
	var fresh []Section
	for _, s := range h.Sections {
		if s.Kind == Version && !known[s.Version] {
			fresh = append(fresh, s)
		}
	}
	if len(fresh) > 1 {
		var names []string
		for _, s := range fresh {
			names = append(names, s.Version)
		}
		problems = append(problems, fmt.Errorf("names %d new versions (%s): a merge releases one", len(fresh), strings.Join(names, ", ")))
	}
	for _, s := range fresh {
		problems = append(problems, newVersionProblems(s, highest, tags)...)
	}
	if len(problems) > 0 || len(fresh) == 0 {
		return "", errors.Join(problems...)
	}
	return fresh[0].Version, nil
}

func newVersionProblems(s Section, highest string, tags []string) []error {
	var problems []error
	if strings.TrimSpace(s.Body) == "" {
		problems = append(problems, fmt.Errorf("line %d: %s is empty: its entries are the release notes", s.Line, s.Heading))
	}
	for _, t := range tags {
		if semver.IsValid(t) && semver.Compare(t, s.Version) == 0 {
			return append(problems, fmt.Errorf("%s is already released (tag %s)", s.Version, t))
		}
	}
	if highest != "" && semver.Compare(s.Version, highest) <= 0 {
		problems = append(problems, fmt.Errorf("%s is not greater than %s, the newest version tagged or named on the base", s.Version, highest))
	}
	return problems
}

func maxVersion(a, b string) string {
	if a == "" || semver.Compare(b, a) > 0 {
		return b
	}
	return a
}
