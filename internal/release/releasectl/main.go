// Command releasectl runs internal/release for `make release-check` and the
// Release and Retry workflows (DESIGN A119):
//
//	releasectl check   [--base REV] [--head REV]
//	releasectl decide  --event E [--tag T] [--commit SHA] [--ci-run ID --ci-attempt N] --attempt N
//	releasectl tag     --tag T --commit SHA
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
	tags, err := repo.Tags("")
	if err != nil {
		return err
	}
	v, err := release.CheckVersion(headText, baseText, tags)
	switch {
	case err != nil:
		return err
	case v == "":
		fmt.Println("CHANGELOG.md names no new version: merging this releases nothing.")
	default:
		fmt.Printf("CHANGELOG.md names %s: merging this tags and publishes it, with its section as the notes.\n", v)
	}
	return nil
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
	commit := fs.String("commit", "", "the merge commit CI completed on")
	ciRun := fs.Int64("ci-run", 0, "CI's run id")
	ciAttempt := fs.Int("ci-attempt", 0, "the CI attempt that completed")
	attempt := fs.Int("attempt", 1, "this run's attempt")
	_ = fs.Parse(args)
	api, err := release.ClientFromEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()
	f := release.Facts{Attempt: *attempt}
	switch *event {
	case "workflow_run":
		f.Mode, f.Commit = release.ModeMerge, repo.Commit(*commit)
		if f.Commit == "" {
			return fmt.Errorf("no commit %q", *commit)
		}
		if f.Version, err = release.VersionAt(repo, f.Commit); err != nil {
			return err
		}
	case "push", "workflow_dispatch":
		f.Mode, f.Version = release.ModeTag, *tagName
		if *event == "workflow_dispatch" {
			f.Mode = release.ModeDispatch
		}
		f.Commit = repo.TagCommit(f.Version)
		_, f.HasNotes = release.NotesAt(repo, f.Commit, f.Version)
	default:
		return fmt.Errorf("unknown event %q", *event)
	}
	if f.Version != "" {
		f.TagCommit = repo.TagCommit(f.Version)
	}
	f.Gate = func() (release.Verdict, []string, error) { return release.CIGate(ctx, api, *ciRun, *ciAttempt) }
	f.Missing = func() (release.Missing, error) { return release.PublishedMissing(ctx, api, f.Version) }
	plan, err := release.Decide(f, os.Stderr)
	if err != nil {
		return err
	}
	plan.Write(os.Stdout)
	return nil
}

func tag(args []string) error {
	fs := flag.NewFlagSet("tag", flag.ExitOnError)
	name := fs.String("tag", "", "the version to tag")
	commit := fs.String("commit", "", "the merge commit")
	_ = fs.Parse(args)
	created, err := repo.CreateTag(*name, *commit, "origin")
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("tagged %s on %s\n", *name, *commit)
	} else {
		fmt.Printf("%s was already tagged on %s\n", *name, *commit)
	}
	return nil
}

func publish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	name := fs.String("tag", "", "the release's tag")
	commit := fs.String("commit", "", "the tag's commit, whose CHANGELOG.md holds the notes")
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
	p.Notes, p.HasNotes = release.NotesAt(repo, *commit, *name)
	if !p.HasNotes {
		fmt.Printf("CHANGELOG.md at %s has no section %s: the release keeps its notes or gets the placeholder\n", *commit, *name)
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
