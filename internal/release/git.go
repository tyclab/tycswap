package release

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Git runs git in a checkout.
type Git struct{ Dir string }

func (g Git) run(args ...string) (string, error) { return g.runEnv(nil, args...) }

func (g Git) runEnv(env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Commit resolves rev to a commit id, "" when there is no such commit.
func (g Git) Commit(rev string) string {
	out, err := g.run("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// TagCommit is the commit tag points at, "" when the tag does not exist.
func (g Git) TagCommit(tag string) string { return g.Commit("refs/tags/" + tag) }

// Changelog is CHANGELOG.md at rev, "" when rev has none.
func (g Git) Changelog(rev string) (string, error) {
	if out, err := g.run("ls-tree", "--name-only", rev, "--", "CHANGELOG.md"); err != nil || strings.TrimSpace(out) == "" {
		return "", err
	}
	return g.run("show", rev+":CHANGELOG.md")
}

// Tags lists the v* tags, only those reachable from merged when it is set.
func (g Git) Tags(merged string) ([]string, error) {
	args := []string{"tag", "--list", "v*"}
	if merged != "" {
		args = append(args, "--merged", merged)
	}
	out, err := g.run(args...)
	return strings.Fields(out), err
}

// Tagger is who annotates the release tags.
const Tagger = "github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>"

// CreateTag annotates commit as tag ("tycswap <tag>", as every release tag
// is) and pushes it to remote. A tag already on commit, here or on remote,
// is success; a tag on another commit is an error.
func (g Git) CreateTag(tag, commit, remote string) (created bool, err error) {
	at := g.TagCommit(tag)
	if at != "" {
		if at != commit {
			return false, fmt.Errorf("tag %s exists on %s, not on %s", tag, short(at), short(commit))
		}
	} else {
		name, email, _ := strings.Cut(strings.TrimSuffix(Tagger, ">"), " <")
		if _, err := g.runEnv([]string{"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email}, "tag", "-a", tag, "-m", "tycswap "+tag, commit); err != nil {
			return false, err
		}
	}
	_, pushErr := g.run("push", remote, "refs/tags/"+tag)
	if pushErr == nil {
		return at == "", nil
	}
	// Someone else's tag of that name won: it is fine only on the same commit.
	_, _ = g.run("tag", "-d", tag)
	if _, err := g.run("fetch", "--no-tags", remote, "+refs/tags/"+tag+":refs/tags/"+tag); err != nil {
		return false, pushErr
	}
	if at = g.TagCommit(tag); at != commit {
		return false, fmt.Errorf("tag %s exists on %s, not on %s", tag, short(at), short(commit))
	}
	return false, nil
}
