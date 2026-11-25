// Command releasectl runs internal/release for `make release-check` and the
// Release and Retry workflows (DESIGN A119, A121):
//
//	releasectl check   [--base REV] [--head REV]
//	releasectl decide  --event E [--tag T] [--commit SHA] --attempt N
//	releasectl tag     --commit SHA --attempt N
//	releasectl publish --tag T --commit SHA --dist DIR [--publish]
//	releasectl retry   --run ID --attempt N
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/tyclab/tycswap/internal/release"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: releasectl check|decide|tag|publish|retry [flags]")
		os.Exit(2)
	}
	cmds := map[string]func([]string) error{"check": check, "decide": decide, "tag": tag, "publish": publish, "retry": retry}
	run, ok := cmds[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "releasectl: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
	if err := run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "releasectl "+os.Args[1]+": "+strings.ReplaceAll(err.Error(), "\n", "\n  "))
		os.Exit(1)
	}
}

var repo = release.Git{Dir: "."}

func check(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	base := fs.String("base", "origin/main", "the commit the change merges into")
	head := fs.String("head", "", "the change's commit (default: the working tree's CHANGELOG.md)")
	_ = fs.Parse(args)
	headText, err := readHead(*head)
	if err != nil {
		return err
	}
	if repo.Commit(*base) == "" {
		return fmt.Errorf("no commit %s: fetch it first (git fetch origin --tags)", *base)
	}
	baseText, err := repo.Changelog(*base)
	if err != nil {
		return err
	}
	since, err := repo.NewestTag(*base)
	if err != nil {
		return err
	}
	tagged := ""
	if since != "" {
		if tagged, err = repo.Changelog(since); err != nil {
			return err
		}
	}
	p, err := release.CheckChange(headText, baseText, tagged, since)
	if err != nil {
		return err
	}
	if len(p.Entries) == 0 {
		fmt.Printf("Merging this releases nothing: CHANGELOG.md has no entry that %s lacks.\n", orNoTag(since))
		return nil
	}
	these := "this entry"
	if len(p.Entries) > 1 {
		these = fmt.Sprintf("these %d entries", len(p.Entries))
	}
	fmt.Printf("Merging this releases %s (%s over %s: %s), with %s as its notes:\n\n%s\n",
		p.Version, p.Bump, orNoTag(since), bumpReason(p.Bump), these, p.Notes())
	return nil
}

func orNoTag(tag string) string {
	if tag == "" {
		return "no tag"
	}
	return tag
}

func bumpReason(bump string) string {
	if bump == "patch" {
		return "every new entry is **Fixed:**"
	}
	return "not every new entry is **Fixed:**"
}

func readHead(rev string) (string, error) {
	if rev != "" {
		return repo.Changelog(rev)
	}
	b, err := os.ReadFile("CHANGELOG.md")
	return string(b), err
}

func decide(args []string) error {
	fs := flag.NewFlagSet("decide", flag.ExitOnError)
	event := fs.String("event", "", "workflow_run, push or workflow_dispatch")
	tagName := fs.String("tag", "", "the tag pushed or asked for")
	commit := fs.String("commit", "", "the merge commit CI or macOS completed on")
	attempt := fs.Int("attempt", 1, "this run's attempt")
	// A rerun of a Release run from before A121 still passes these.
	fs.Int64("ci-run", 0, "ignored")
	fs.Int("ci-attempt", 0, "ignored")
	_ = fs.Parse(args)
	api, err := release.ClientFromEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()
	f := release.Facts{Attempt: *attempt}
	switch *event {
	case "workflow_run":
		sha := repo.Commit(*commit)
		if sha == "" {
			return fmt.Errorf("no commit %q", *commit)
		}
		if f, err = release.MergeFacts(repo, sha, *attempt); err != nil {
			return err
		}
	case "push", "workflow_dispatch":
		f.Mode, f.Version = release.ModeTag, *tagName
		if *event == "workflow_dispatch" {
			f.Mode = release.ModeDispatch
		}
		f.Commit = repo.TagCommit(f.Version)
		f.TagCommit = f.Commit
		_, f.HasNotes = release.NotesAt(repo, f.Commit)
	default:
		return fmt.Errorf("unknown event %q", *event)
	}
	f.Gate = func() (release.Verdict, []string, error) { return release.ReleaseGate(ctx, api, f.Commit) }
	f.Missing = func() (release.Missing, error) { return release.PublishedMissing(ctx, api, f.Version) }
	plan, err := release.Decide(f, os.Stderr)
	if err != nil {
		return err
	}
	plan.Write(os.Stdout)
	return nil
}

// tag prints the tag job's outputs: the release's tag and whether this run
// builds and publishes it.
func tag(args []string) error {
	fs := flag.NewFlagSet("tag", flag.ExitOnError)
	commit := fs.String("commit", "", "the merge commit")
	attempt := fs.Int("attempt", 1, "this run's attempt")
	fs.String("tag", "", "ignored: the version is computed again (a Release run from before A121 passes it)")
	_ = fs.Parse(args)
	c, err := release.ClaimTag(repo, *commit, "origin", *attempt, os.Stderr)
	if err != nil {
		return err
	}
	fmt.Printf("tag=%s\nrelease=%t\n", c.Tag, c.Release)
	return nil
}

func publish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	name := fs.String("tag", "", "the release's tag")
	commit := fs.String("commit", "", "the tag's commit, whose new entries are the notes")
	dir := fs.String("dist", "dist", "the builds to attach")
	pub := fs.Bool("publish", false, "publish the draft once Linux and Windows are attached")
	_ = fs.Parse(args)
	if at := repo.TagCommit(*name); at == "" || at != repo.Commit(*commit) {
		return fmt.Errorf("tag %s is not on %s", *name, *commit)
	}
	api, err := release.ClientFromEnv()
	if err != nil {
		return err
	}
	p := release.Publisher{API: api, Tag: *name, Publish: *pub, Dist: *dir, Log: os.Stdout}
	p.Notes, p.HasNotes = release.NotesAt(repo, *commit)
	if !p.HasNotes {
		fmt.Printf("CHANGELOG.md at %s adds no entry over the tag before it: the release keeps its notes or gets the placeholder\n", *commit)
	}
	return p.Run(context.Background())
}

func retry(args []string) error {
	fs := flag.NewFlagSet("retry", flag.ExitOnError)
	runID := fs.Int64("run", 0, "the completed run")
	attempt := fs.Int("attempt", 0, "the attempt that completed")
	_ = fs.Parse(args)
	api, err := release.ClientFromEnv()
	if err != nil {
		return err
	}
	return release.Retry(context.Background(), api, *runID, *attempt, os.Stdout)
}
