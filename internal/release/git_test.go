package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepos makes a bare origin and two clones with three commits, the
// second and third carrying CHANGELOG.md; git's global and system
// configuration stay out.
func gitRepos(t *testing.T) (origin string, a, b Git, commits []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.invalid"} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	must := func(g Git, args ...string) string {
		t.Helper()
		out, err := g.run(args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	origin = filepath.Join(dir, "origin.git")
	must(Git{dir}, "init", "--quiet", "--bare", "--initial-branch=main", origin)
	a, b = Git{filepath.Join(dir, "a")}, Git{filepath.Join(dir, "b")}
	must(Git{dir}, "clone", "--quiet", origin, a.Dir)
	for i, text := range []string{"", v0233, introduced} {
		name := "README"
		if text != "" {
			name = "CHANGELOG.md"
		}
		if err := os.WriteFile(filepath.Join(a.Dir, name), []byte(text+strings.Repeat("x", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		must(a, "add", "-A")
		must(a, "commit", "--quiet", "-m", "c")
		commits = append(commits, must(a, "rev-parse", "HEAD"))
	}
	must(a, "push", "--quiet", "origin", "HEAD:main")
	must(Git{dir}, "clone", "--quiet", origin, b.Dir)
	return origin, a, b, commits
}

func TestCreateTag(t *testing.T) {
	_, a, b, commits := gitRepos(t)
	head := commits[2]
	created, err := a.CreateTag("v0.24.0", head, "origin")
	if err != nil || !created {
		t.Fatalf("CreateTag = %v, %v", created, err)
	}
	obj, _ := a.run("cat-file", "-p", "refs/tags/v0.24.0")
	if !strings.Contains(obj, "type commit") || !strings.Contains(obj, "tagger github-actions[bot]") || !strings.HasSuffix(strings.TrimSpace(obj), "tycswap v0.24.0") {
		t.Errorf("tag object:\n%s", obj)
	}
	if created, err := a.CreateTag("v0.24.0", head, "origin"); err != nil || created {
		t.Errorf("again: %v, %v", created, err)
	}
	// b has not fetched the tag: its own tag object (another date) is
	// refused, and the tag it then fetches is on the same commit.
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-02T03:04:05Z")
	if created, err := b.CreateTag("v0.24.0", head, "origin"); err != nil || created {
		t.Errorf("from the other clone: %v, %v", created, err)
	}
	if _, err := b.CreateTag("v0.24.0", commits[1], "origin"); err == nil || !strings.Contains(err.Error(), "exists on") {
		t.Errorf("on another commit: %v", err)
	}
	if _, err := a.CreateTag("v0.24.0", commits[1], "origin"); err == nil {
		t.Error("a local tag on another commit was accepted")
	}
}

func TestGitReads(t *testing.T) {
	_, a, _, commits := gitRepos(t)
	if text, err := a.Changelog(commits[0]); err != nil || text != "" {
		t.Errorf("a commit without CHANGELOG.md: %q, %v", text, err)
	}
	if text, err := a.Changelog(commits[2]); err != nil || !strings.HasPrefix(text, introduced) {
		t.Errorf("CHANGELOG.md: %q, %v", text, err)
	}
	if _, err := a.run("tag", "v0.1.0", commits[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.run("tag", "v0.2.0", commits[2]); err != nil {
		t.Fatal(err)
	}
	all, _ := a.Tags("")
	merged, _ := a.Tags(commits[1])
	if strings.Join(all, ",") != "v0.1.0,v0.2.0" || strings.Join(merged, ",") != "v0.1.0" {
		t.Errorf("tags %v, merged into the parent %v", all, merged)
	}
	if a.TagCommit("v0.2.0") != commits[2] || a.TagCommit("v9.9.9") != "" || a.Commit(commits[2]+"^1") != commits[1] {
		t.Error("rev-parse answers")
	}
}
