package release

import (
	"strings"
	"testing"
)

// v0233 is the CHANGELOG as main had it at v0.23.3: every entry under one
// Unreleased heading.
const v0233 = "# Changelog\n\n## Unreleased\n\n- **Fixed:** the lock wait.\n- older entry\n"

// introduced is the change that introduces versions: the history keeps its
// words under the legacy heading and the change names its version.
const introduced = "# Changelog\n\n## Unreleased\n\n## v0.24.0\n\n- Releases publish themselves.\n\n" +
	LegacyHeading + "\n\n- **Fixed:** the lock wait.\n- older entry\n"

var tagsTo0233 = []string{"v0.1.0", "v0.9.0", "v0.10.0", "v0.23.2", "v0.23.3"}

// withVersion puts a version section right under ## Unreleased: what was
// under Unreleased moves into it, as a releasing change renames the heading.
func withVersion(base, version, notes string) string {
	return strings.Replace(base, "## Unreleased\n\n", "## Unreleased\n\n## "+version+"\n\n"+notes+"\n\n", 1)
}

func TestCheckVersion(t *testing.T) {
	released := introduced // main after v0.24.0 merged
	tags024 := append(append([]string{}, tagsTo0233...), "v0.24.0")
	cases := []struct {
		name       string
		head, base string
		tags       []string
		want       string // the new version, "" for none
		wantErr    string
	}{
		{name: "the introducing change names v0.24.0", head: introduced, base: v0233, tags: tagsTo0233, want: "v0.24.0"},
		{name: "no new version", head: released, base: released, tags: tags024},
		{name: "entries under Unreleased only", head: strings.Replace(released, "## Unreleased\n\n", "## Unreleased\n\n- next\n\n", 1), base: released, tags: tags024},
		{name: "a patch release", head: withVersion(released, "v0.24.1", "- fix"), base: released, tags: tags024, want: "v0.24.1"},
		{name: "a major release", head: withVersion(released, "v1.0.0", "- big"), base: released, tags: tags024, want: "v1.0.0"},
		{name: "nested markup", head: withVersion(released, "v0.25.0", notesWithMarkup), base: released, tags: tags024, want: "v0.25.0"},
		{name: "version not greater than the newest tag", head: withVersion(v0233, "v0.23.1", "- x"), base: v0233, tags: tagsTo0233,
			wantErr: "v0.23.1 is not greater than v0.23.3"},
		{name: "numeric not lexical order", head: withVersion(v0233, "v0.9.1", "- x"), base: v0233, tags: tagsTo0233,
			wantErr: "v0.9.1 is not greater than v0.23.3"},
		{name: "repeats a released version", head: withVersion(v0233, "v0.23.3", "- x"), base: v0233, tags: tagsTo0233,
			wantErr: "v0.23.3 is already released (tag v0.23.3)"},
		{name: "empty section", head: withVersion(released, "v0.25.0", ""), base: released, tags: tags024,
			wantErr: "## v0.25.0 is empty"},
		{name: "blank-only section", head: withVersion(released, "v0.25.0", "  \n\t"), base: released, tags: tags024,
			wantErr: "## v0.25.0 is empty"},
		{name: "not vMAJOR.MINOR.PATCH", head: strings.Replace(introduced, "## v0.24.0", "## v0.24", 1), base: v0233, tags: tagsTo0233,
			wantErr: `"## v0.24" is not a section heading`},
		{name: "two new versions", head: withVersion(introduced, "v0.24.1", "- y"), base: v0233, tags: tagsTo0233,
			wantErr: "names 2 new versions (v0.24.1, v0.24.0)"},
		{name: "changes a tagged section", head: strings.Replace(released, "- Releases publish themselves.", "- Edited.", 1), base: released, tags: tags024,
			wantErr: "changes ## v0.24.0, which the base already has"},
		{name: "removes a tagged section", head: strings.Replace(released, "## v0.24.0\n\n- Releases publish themselves.\n\n", "", 1), base: released, tags: tags024,
			wantErr: "removes ## v0.24.0, which the base already has"},
		{name: "changes the history", head: strings.Replace(released, "- older entry", "- rewritten", 1), base: released, tags: tags024,
			wantErr: "changes " + LegacyHeading},
		{name: "a version main names but has not tagged yet still counts", head: withVersion(released, "v0.24.0", "- dup"), base: released, tags: tagsTo0233,
			wantErr: "names ## v0.24.0 twice"},
		{name: "below a version main names but has not tagged", head: strings.Replace(released, "## v0.24.0", "## v0.23.9\n\n- mine\n\n## v0.24.0", 1), base: released, tags: tagsTo0233,
			wantErr: "## v0.24.0 stands below ## v0.23.9"},
		{name: "a broken base is named", head: introduced, base: "## v1\n", tags: tagsTo0233, wantErr: "the base's CHANGELOG.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckVersion(tc.head, tc.base, tc.tags)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("new version %q, want %q", got, tc.want)
			}
		})
	}
}

// Two pull requests that claim the same version: the second must merge
// main first (the ruleset wants an up-to-date branch), and its check then
// meets the first one's section on the base.
func TestTheSecondClaimOfAVersionFailsBeforeItsMerge(t *testing.T) {
	first := withVersion(v0233, "v0.24.0", "- first")
	second := withVersion(v0233, "v0.24.0", "- second")
	if v, err := CheckVersion(second, v0233, tagsTo0233); err != nil || v != "v0.24.0" {
		t.Fatalf("before the first merged: %q, %v", v, err)
	}
	// After main moved, git merges both headings into one section or
	// conflicts; either way the check refuses what results.
	merged := strings.Replace(first, "- first", "- first\n- second", 1)
	if _, err := CheckVersion(merged, first, tagsTo0233); err == nil || !strings.Contains(err.Error(), "changes ## v0.24.0") {
		t.Errorf("one section: %v", err)
	}
	both := strings.Replace(first, "## Unreleased\n\n", "## Unreleased\n\n## v0.24.0\n\n- second\n\n", 1)
	if _, err := CheckVersion(both, first, tagsTo0233); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("two sections: %v", err)
	}
}

func TestCheckCountsEveryProblem(t *testing.T) {
	empty := "# Changelog\n\n## Unreleased\n\n" + LegacyHeading + "\n\n- old\n"
	head := withVersion(withVersion(empty, "v0.1.0", ""), "v0.23.0", "- x")
	_, err := CheckVersion(head, empty, tagsTo0233)
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"names 2 new versions", "## v0.1.0 is empty", "v0.1.0 is already released", "v0.23.0 is not greater"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
