package release

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// mergeHistory is main with v0.25.0 on its first commit and one commit per
// later CHANGELOG.md text: the merges whose releases race.
func mergeHistory(t *testing.T, changelogs ...string) (a, b Git, commits []string) {
	t.Helper()
	_, a, b, commits = gitHistory(t, append([]string{since("- e0")}, changelogs...)...)
	if _, err := a.CreateTag("v0.25.0", commits[0], "origin"); err != nil {
		t.Fatal(err)
	}
	return a, b, commits
}

func claim(t *testing.T, g Git, commit string, attempt int) Claim {
	t.Helper()
	c, err := ClaimTag(g, commit, "origin", attempt, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// decided is the plan of a fresh Release run for commit whose CI passed.
func decided(t *testing.T, g Git, commit string) Plan {
	t.Helper()
	if err := g.FetchTags("origin"); err != nil {
		t.Fatal(err)
	}
	f, err := MergeFacts(g, commit, 1)
	if err != nil {
		t.Fatal(err)
	}
	f.Gate = gateSays(Pass)
	f.Missing = func() (Missing, error) { t.Error("the release was asked"); return Missing{}, nil }
	plan, err := Decide(f, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// prePush makes g's next push run script first, once: another run's push
// landing between this run's look at the tags and its own push.
func prePush(t *testing.T, g Git, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the race is staged with a POSIX shell hook")
	}
	hooks := filepath.Join(g.Dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	mark := filepath.Join(t.TempDir(), "ran")
	hook := "#!/bin/sh\n[ -e '" + mark + "' ] && exit 0\n: > '" + mark + "'\nunset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
}

func tagOn(t *testing.T, g Git, commit string) string {
	t.Helper()
	tags, err := g.TagsAt(commit)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(tags, ",")
}

const (
	fixA = "- **Fixed:** a"
	fixB = "- **Fixed:** b"
)

// Two merges A and B (B's first parent A) whose Release runs come in either
// order: one release carries every entry no tag has, and the other run does
// nothing, quietly.
func TestMergeOrders(t *testing.T) {
	t.Run("A, then B", func(t *testing.T) {
		a, b, c := mergeHistory(t, since(fixA, "- e0"), since("- b", fixA, "- e0"))
		if p := decided(t, a, c[1]); !p.CreateTag || p.Tag != "v0.25.1" {
			t.Fatalf("A: %+v", p)
		}
		if got := claim(t, a, c[1], 1); got != (Claim{"v0.25.1", true}) {
			t.Fatalf("A claimed %+v", got)
		}
		if p := decided(t, b, c[2]); !p.CreateTag || p.Tag != "v0.26.0" {
			t.Fatalf("B: %+v", p)
		}
		if got := claim(t, b, c[2], 1); got != (Claim{"v0.26.0", true}) {
			t.Fatalf("B claimed %+v", got)
		}
		if notes, _ := NotesAt(b, c[2]); notes != "- b" {
			t.Errorf("B's notes %q", notes)
		}
	})
	t.Run("B, then A: B's release carries A's entry", func(t *testing.T) {
		a, b, c := mergeHistory(t, since(fixA, "- e0"), since("- b", fixA, "- e0"))
		if got := claim(t, b, c[2], 1); got != (Claim{"v0.26.0", true}) {
			t.Fatalf("B claimed %+v", got)
		}
		if notes, _ := NotesAt(b, c[2]); notes != "- b\n"+fixA {
			t.Errorf("B's notes %q", notes)
		}
		if p := decided(t, a, c[1]); p != (Plan{}) {
			t.Errorf("A after B: %+v", p)
		}
		if got := claim(t, a, c[1], 1); got != (Claim{}) || tagOn(t, a, c[1]) != "" {
			t.Errorf("A claimed %+v", got)
		}
	})
	t.Run("B, then A, both fixes: the name A computes is B's tag", func(t *testing.T) {
		a, b, c := mergeHistory(t, since(fixA, "- e0"), since(fixB, fixA, "- e0"))
		if got := claim(t, b, c[2], 1); got != (Claim{"v0.25.1", true}) {
			t.Fatalf("B claimed %+v", got)
		}
		if err := a.FetchTags("origin"); err != nil {
			t.Fatal(err)
		}
		f, err := MergeFacts(a, c[1], 1)
		if err != nil || f.Version != "v0.25.1" || f.CarriedBy != "v0.25.1" {
			t.Fatalf("A's facts %+v, %v", f, err)
		}
		if p := decided(t, a, c[1]); p != (Plan{}) {
			t.Errorf("A after B: %+v", p)
		}
		if got := claim(t, a, c[1], 1); got != (Claim{}) {
			t.Errorf("A claimed %+v", got)
		}
	})
	t.Run("C, then B, then A", func(t *testing.T) {
		a, b, c := mergeHistory(t, since(fixA, "- e0"), since(fixB, fixA, "- e0"), since("- c", fixB, fixA, "- e0"))
		if got := claim(t, b, c[3], 1); got != (Claim{"v0.26.0", true}) {
			t.Fatalf("C claimed %+v", got)
		}
		for _, i := range []int{2, 1} {
			if p := decided(t, a, c[i]); p != (Plan{}) {
				t.Errorf("merge %d after C: %+v", i, p)
			}
		}
		if notes, _ := NotesAt(a, c[3]); notes != "- c\n"+fixB+"\n"+fixA {
			t.Errorf("C's notes %q", notes)
		}
	})
	t.Run("a merge without an entry", func(t *testing.T) {
		a, _, c := mergeHistory(t, since(fixA, "- e0"), since(fixA, "- e0"))
		claim(t, a, c[1], 1)
		if p := decided(t, a, c[2]); p != (Plan{}) {
			t.Errorf("plan %+v", p)
		}
		if got := claim(t, a, c[2], 1); got != (Claim{}) {
			t.Errorf("claimed %+v", got)
		}
	})
}

// Two Release runs tag at the same moment: the tag job computes again from
// what the other run pushed.
func TestTagRaces(t *testing.T) {
	t.Run("A's run takes the name B computed: B's version moves past it", func(t *testing.T) {
		a, b, c := mergeHistory(t, since(fixA, "- e0"), since(fixB, fixA, "- e0"))
		prePush(t, b, "git -C '"+a.Dir+"' tag -a v0.25.1 -m 'tycswap v0.25.1' "+c[1]+" && git -C '"+a.Dir+"' push --quiet origin refs/tags/v0.25.1")
		if got := claim(t, b, c[2], 1); got != (Claim{"v0.25.2", true}) {
			t.Fatalf("B claimed %+v", got)
		}
		if tagOn(t, b, c[1]) != "v0.25.1" || tagOn(t, b, c[2]) != "v0.25.2" {
			t.Errorf("tags %s on A, %s on B", tagOn(t, b, c[1]), tagOn(t, b, c[2]))
		}
		if notes, _ := NotesAt(b, c[2]); notes != fixB {
			t.Errorf("B's notes %q", notes)
		}
	})
	t.Run("B's run takes the name A computed: no error, A's entry is B's", func(t *testing.T) {
		a, b, c := mergeHistory(t, since(fixA, "- e0"), since(fixB, fixA, "- e0"))
		prePush(t, a, "git -C '"+b.Dir+"' tag -a v0.25.1 -m 'tycswap v0.25.1' "+c[2]+" && git -C '"+b.Dir+"' push --quiet origin refs/tags/v0.25.1")
		if got := claim(t, a, c[1], 1); got != (Claim{}) {
			t.Fatalf("A claimed %+v", got)
		}
		if tagOn(t, a, c[1]) != "" || tagOn(t, a, c[2]) != "v0.25.1" {
			t.Errorf("tags %q on A, %q on B", tagOn(t, a, c[1]), tagOn(t, a, c[2]))
		}
	})
	t.Run("the CI and macOS runs of one merge: the second leaves it to the first", func(t *testing.T) {
		a, b, c := mergeHistory(t, since(fixA, "- e0"))
		if got := claim(t, a, c[1], 1); got != (Claim{"v0.25.1", true}) {
			t.Fatalf("first claimed %+v", got)
		}
		if got := claim(t, b, c[1], 1); got != (Claim{"v0.25.1", false}) {
			t.Errorf("second claimed %+v", got)
		}
		if got := claim(t, b, c[1], 2); got != (Claim{"v0.25.1", true}) {
			t.Errorf("a rerun claimed %+v", got)
		}
	})
	t.Run("both push the same tag at once", func(t *testing.T) {
		a, b, c := mergeHistory(t, since(fixA, "- e0"))
		prePush(t, b, "git -C '"+a.Dir+"' tag -a v0.25.1 -m 'tycswap v0.25.1' "+c[1]+" && git -C '"+a.Dir+"' push --quiet origin refs/tags/v0.25.1")
		if got := claim(t, b, c[1], 1); got != (Claim{"v0.25.1", false}) {
			t.Errorf("claimed %+v", got)
		}
	})
	t.Run("the name on a commit that neither precedes nor carries the merge", func(t *testing.T) {
		a, _, c := mergeHistory(t, since(fixA, "- e0"))
		if _, err := a.run("checkout", "--quiet", "-b", "side", c[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := a.run("commit", "--quiet", "--allow-empty", "-m", "side"); err != nil {
			t.Fatal(err)
		}
		side := a.Commit("HEAD")
		if _, err := a.CreateTag("v0.25.1", side, "origin"); err != nil {
			t.Fatal(err)
		}
		f, err := MergeFacts(a, c[1], 1)
		if err != nil {
			t.Fatal(err)
		}
		f.Gate, f.Missing = unasked(t)
		if _, err := Decide(f, io.Discard); err == nil || !strings.Contains(err.Error(), "tag v0.25.1 points at") {
			t.Errorf("Decide error %v", err)
		}
		if _, err := ClaimTag(a, c[1], "origin", 1, io.Discard); err == nil || !strings.Contains(err.Error(), "neither is") {
			t.Errorf("ClaimTag error %v", err)
		}
	})
}

// This change's own merge, while main's CHANGELOG.md still has ## Unreleased
// above the v0.24.1 section: with v0.24.1 tagged it releases its own entry;
// had v0.24.1 not been tagged yet, its release carries v0.24.1's entry too,
// and the v0.24.1 merge's run then does nothing.
func TestTheTransitionMerge(t *testing.T) {
	for _, tagged := range []bool{true, false} {
		a, _, c := func() (Git, Git, []string) {
			_, a, b, c := gitHistory(t, v0240, v0241, since("- Every merge releases."))
			if _, err := a.CreateTag("v0.24.0", c[0], "origin"); err != nil {
				t.Fatal(err)
			}
			if tagged {
				if _, err := a.CreateTag("v0.24.1", c[1], "origin"); err != nil {
					t.Fatal(err)
				}
			}
			return a, b, c
		}()
		want, notes := "v0.25.0", "- Every merge releases."
		if !tagged {
			notes += "\n- **Fixed:** start login."
		}
		if got := claim(t, a, c[2], 1); got != (Claim{want, true}) {
			t.Fatalf("tagged %v: claimed %+v", tagged, got)
		}
		if n, _ := NotesAt(a, c[2]); n != notes {
			t.Errorf("tagged %v: notes %q", tagged, n)
		}
		if !tagged {
			if p := decided(t, a, c[1]); p != (Plan{}) {
				t.Errorf("the v0.24.1 merge after this one: %+v", p)
			}
		}
	}
}
