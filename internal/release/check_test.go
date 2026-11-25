package release

import (
	"strings"
	"testing"
)

// history is what stays below the top section from A121 on.
const history = "## v0.24.1\n\n- **Fixed:** start login.\n\n## v0.24.0\n\n- Releases publish themselves.\n\n" +
	LegacyHeading + "\n\n- **Fixed:** the lock wait.\n- older entry\n"

// v0241 and v0240 are CHANGELOG.md at those tags, before A121.
const (
	v0241 = "# Changelog\n\n## Unreleased\n\n" + history
	v0240 = "# Changelog\n\n## Unreleased\n\n## v0.24.0\n\n- Releases publish themselves.\n\n" +
		LegacyHeading + "\n\n- **Fixed:** the lock wait.\n- older entry\n"
)

// since is CHANGELOG.md from A121 on with entries in the top section,
// newest first.
func since(entries ...string) string {
	top := ""
	if len(entries) > 0 {
		top = strings.Join(entries, "\n") + "\n\n"
	}
	return "# Changelog\n\n" + TopHeading + "\n\n" + top + history
}

func parsed(t *testing.T, text string) Changelog {
	t.Helper()
	c, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func texts(entries []Entry) string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Text)
	}
	return strings.Join(out, "|")
}

func TestNewEntries(t *testing.T) {
	cases := []struct {
		name, head, tagged, want string
	}{
		{"this change over v0.24.1", since("- Every merge releases."), v0241, "- Every merge releases."},
		{"over v0.24.0, before v0.24.1 is tagged", since("- Every merge releases."), v0240, "- Every merge releases.|- **Fixed:** start login."},
		{"nothing new", v0241, v0241, ""},
		{"what a later tag has", since("- c", "- b", "- a"), since("- b", "- a"), "- c"},
		{"an entry below older ones", since("- b", "- c", "- a"), since("- b", "- a"), "- c"},
		{"of two equal entries the upper is new", since("- dup", "- x", "- dup"), since("- x", "- dup"), "- dup"},
		{"trailing white space is no change", since("- a  "), since("- a"), ""},
		{"a reordered entry is no new one", since("- a", "- b"), since("- b", "- a"), ""},
		{"a changed entry is a new one", since("- a, reworded"), since("- a"), "- a, reworded"},
		{"no tag yet", since("- a"), "", "- a|- **Fixed:** start login.|- Releases publish themselves.|- **Fixed:** the lock wait.|- older entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := texts(NewEntries(parsed(t, tc.head), parsed(t, tc.tagged))); got != tc.want {
				t.Errorf("NewEntries = %q, want %q", got, tc.want)
			}
		})
	}
	dup := NewEntries(parsed(t, since("- dup", "- x", "- dup")), parsed(t, since("- x", "- dup")))
	if dup[0].Line != 5 {
		t.Errorf("the new one of two equal entries is at line %d, want 5", dup[0].Line)
	}
}

func TestNextVersion(t *testing.T) {
	fix, other := Entry{Text: "- **Fixed:** x"}, Entry{Text: "- x"}
	cases := []struct {
		since   string
		entries []Entry
		want    string
		bump    string
	}{
		{"v0.24.1", []Entry{fix}, "v0.24.2", "patch"},
		{"v0.24.1", []Entry{fix, fix}, "v0.24.2", "patch"},
		{"v0.24.1", []Entry{fix, other}, "v0.25.0", "minor"},
		{"v0.24.1", []Entry{other}, "v0.25.0", "minor"},
		{"v0.9.3", []Entry{other}, "v0.10.0", "minor"},
		{"v1.2.3", []Entry{{Text: "- **Added:** big"}}, "v1.3.0", "minor"},
		{"v0.24.1", []Entry{{Text: "- Also **Fixed:** x"}}, "v0.25.0", "minor"},
		{"v0.24.1", []Entry{{Text: "-  **Fixed:** x"}}, "v0.25.0", "minor"},
		{"", []Entry{fix}, "v0.0.1", "patch"},
	}
	for _, tc := range cases {
		if got, bump := NextVersion(tc.since, tc.entries); got != tc.want || bump != tc.bump {
			t.Errorf("NextVersion(%q, %v) = %s %s, want %s %s", tc.since, texts(tc.entries), got, bump, tc.want, tc.bump)
		}
	}
	if n := Newest([]string{"v0.9.0", "v0.10.0", "v0.24.1", "v0.24.0", "nightly"}); n != "v0.24.1" {
		t.Errorf("Newest = %s", n)
	}
}

func TestCheckChange(t *testing.T) {
	withUnreleased := "# Changelog\n\n" + TopHeading + "\n\n- a\n\n## Unreleased\n\n- mine\n\n" + history
	cases := []struct {
		name                       string
		head, base, tagged, since  string
		want, wantEntries, wantErr string
		notErr                     string
	}{
		{name: "this change over v0.24.1", head: since("- Every merge releases."), base: v0241, tagged: v0241, since: "v0.24.1",
			want: "v0.25.0", wantEntries: "- Every merge releases."},
		{name: "this change before v0.24.1 is tagged", head: since("- Every merge releases."), base: v0241, tagged: v0240, since: "v0.24.0",
			want: "v0.25.0", wantEntries: "- Every merge releases.|- **Fixed:** start login."},
		{name: "a fix releases a patch", head: since("- **Fixed:** b", "- a"), base: since("- a"), tagged: since("- a"), since: "v0.25.0",
			want: "v0.25.1", wantEntries: "- **Fixed:** b"},
		{name: "no entry releases nothing", head: since("- a"), base: since("- a"), tagged: since("- a"), since: "v0.25.0"},
		{name: "an entry below older ones", head: since("- a", "- b"), base: since("- a"), tagged: since("- a"), since: "v0.25.0",
			want: "v0.26.0", wantEntries: "- b"},
		{name: "main's entries no tag has go with this merge", head: since("- b", "- a"), base: since("- a"), tagged: v0241, since: "v0.24.1",
			want: "v0.25.0", wantEntries: "- b|- a"},
		{name: "the title is free", head: strings.Replace(since("- b"), "# Changelog", "# tycswap changelog\n\nWhat changed.", 1), base: since(), tagged: since(), since: "v0.25.0",
			want: "v0.26.0", wantEntries: "- b"},
		{name: "## Unreleased is still written", head: withUnreleased, base: since("- a"), tagged: since("- a"), since: "v0.25.0",
			wantErr: "line 7: ## Unreleased is gone (DESIGN A121): move its entries to the top of " + TopHeading + " and delete the heading"},
		{name: "a branch that has not merged main", head: strings.Replace(v0241, "## Unreleased\n\n", "## Unreleased\n\n- mine\n\n", 1), base: since("- a"), tagged: since("- a"), since: "v0.25.0",
			wantErr: "line 3: ## Unreleased is gone", notErr: "changes or removes"},
		{name: "names a version", head: strings.Replace(since("- a"), "\n## v0.24.1", "\n## v0.25.0\n\n- mine\n\n## v0.24.1", 1), base: since("- a"), tagged: since("- a"), since: "v0.24.1",
			wantErr: "line 7: ## v0.25.0 names a version, which a merge no longer does (DESIGN A121): move its entries to the top of " + TopHeading + " and delete the heading"},
		{name: "an entry above the top section", head: strings.Replace(since(), "# Changelog\n\n", "# Changelog\n\n- mine\n\n", 1), base: since(), tagged: since(), since: "v0.25.0",
			wantErr: `line 3: an entry above ` + TopHeading + ` ("- mine"): move it to the top of ` + TopHeading},
		{name: "text in the top section that is no entry", head: since("Some prose.\n\n- a"), base: since("- a"), tagged: since("- a"), since: "v0.25.0",
			wantErr: "line 5: text in " + TopHeading + " that is no entry"},
		{name: "a starred item", head: since("- a\n* b"), base: since("- a"), tagged: since("- a"), since: "v0.25.0",
			wantErr: "line 6: text in " + TopHeading + " that is no entry"},
		{name: "changes a released entry", head: since("- a, reworded"), base: since("- a"), tagged: since("- a"), since: "v0.25.0",
			wantErr: `changes or removes the entry released in v0.25.0 at line 5 of main's CHANGELOG.md ("- a"): a released entry stays as it is; correct it with a new entry at the top of ` + TopHeading},
		{name: "removes a merged entry no tag has yet", head: since(), base: since("- a"), tagged: v0241, since: "v0.24.1",
			wantErr: `changes or removes the entry at line 5 of main's CHANGELOG.md ("- a"), which main's next release carries`},
		{name: "changes the history", head: strings.Replace(since(), "- older entry", "- rewritten", 1), base: since(), tagged: since(), since: "v0.25.0",
			wantErr: "changes " + LegacyHeading + ", which main has: released history stays as it is"},
		{name: "adds an entry to a released version", head: strings.Replace(since(), "- **Fixed:** start login.", "- **Fixed:** start login.\n- late", 1), base: since(), tagged: since(), since: "v0.25.0",
			wantErr: "line 5: changes ## v0.24.1, which main has"},
		{name: "removes a version", head: strings.Replace(since(), "## v0.24.0\n\n- Releases publish themselves.\n\n", "", 1), base: since(), tagged: since(), since: "v0.25.0",
			wantErr: "removes ## v0.24.0, which main has"},
		{name: "no top section", head: "# Changelog\n\n" + history, base: since(), tagged: since(), since: "v0.25.0",
			wantErr: "CHANGELOG.md has no " + TopHeading},
		{name: "a heading CHANGELOG.md does not allow", head: strings.Replace(since("- a"), "- a", "### Added\n## Added\n- a", 1), base: since(), tagged: since(), since: "v0.25.0",
			wantErr: `"## Added" is not a section heading`},
		{name: "a broken base is named", head: since("- a"), base: "## v1\n", tagged: since(), since: "v0.25.0", wantErr: "the base's CHANGELOG.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := CheckChange(tc.head, tc.base, tc.tagged, tc.since)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want one containing %q", err, tc.wantErr)
				}
				if tc.notErr != "" && strings.Contains(err.Error(), tc.notErr) {
					t.Errorf("error %v also says %q", err, tc.notErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Version != tc.want || texts(p.Entries) != tc.wantEntries || p.Since != tc.since {
				t.Errorf("CheckChange = %s %q over %s, want %s %q", p.Version, texts(p.Entries), p.Since, tc.want, tc.wantEntries)
			}
		})
	}
}

func TestCheckCountsEveryProblem(t *testing.T) {
	head := strings.Replace(since("prose", "- a, reworded"), "- older entry", "- rewritten", 1)
	head = strings.Replace(head, "\n## v0.24.1", "\n## v0.25.0\n\n- mine\n\n## v0.24.1", 1)
	_, err := CheckChange(head, since("- a"), since("- a"), "v0.25.0")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"text in " + TopHeading, "changes " + LegacyHeading, "## v0.25.0 names a version", "changes or removes the entry released in v0.25.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// The transition as the repository has it: this file against main's before
// A121, where ## Unreleased stood (empty) above the same history. Its merge
// releases every entry of the top section over v0.24.1, as a minor release
// unless every one is a fix.
func TestTheRepositoryChangelogOverV0241(t *testing.T) {
	head := repositoryChangelog(t)
	c := parsed(t, head)
	top, _ := c.section(Top)
	base := strings.Replace(head, TopHeading+"\n\n"+top.Body+"\n", UnreleasedHeading+"\n", 1)
	if base == head {
		t.Fatal("the top section was not found to take out")
	}
	p, err := CheckChange(head, base, base, "v0.24.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) == 0 || len(p.Entries) != len(top.Entries()) {
		t.Fatalf("%d entries released, the top section has %d", len(p.Entries), len(top.Entries()))
	}
	if want, _ := NextVersion("v0.24.1", top.Entries()); p.Version != want || !strings.HasPrefix(p.Version, "v0.2") {
		t.Errorf("version %s, want %s", p.Version, want)
	}
}
