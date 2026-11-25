package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"golang.org/x/mod/semver"
)

// PendingAt is what a merge commit releases: the entries of its
// CHANGELOG.md that the CHANGELOG.md of the newest v* tag reachable from its
// first parent lacks, with the version they get. A tag a later merge got
// first is not counted.
func PendingAt(g Git, commit string) (Pending, error) {
	since, err := g.NewestTag(commit + "^1")
	if err != nil {
		return Pending{}, err
	}
	head, err := g.changelogAt(commit)
	if err != nil {
		return Pending{}, err
	}
	tagged, err := g.changelogAt(since)
	if err != nil {
		return Pending{}, err
	}
	return PendingOver(head, tagged, since), nil
}

func (g Git) changelogAt(rev string) (Changelog, error) {
	if rev == "" {
		return Changelog{}, nil
	}
	text, err := g.Changelog(rev)
	if err != nil {
		return Changelog{}, err
	}
	c, err := Parse(text)
	if err != nil {
		return Changelog{}, fmt.Errorf("CHANGELOG.md at %s: %w", short(rev), err)
	}
	return c, nil
}

// MergeState is what a merge commit releases and how far that went.
type MergeState struct {
	Pending
	// OnCommit is a v* tag on the commit: its release, whatever its name.
	OnCommit string
	// CarriedBy is a v* tag on a later commit whose CHANGELOG.md has every
	// new entry of this one: that release carries them.
	CarriedBy string
	// TagCommit is the commit a tag named Version is on, "" when there is none.
	TagCommit string
}

// MergeAt reads PendingAt and the v* tags on and after commit.
func MergeAt(g Git, commit string) (MergeState, error) {
	p, err := PendingAt(g, commit)
	st := MergeState{Pending: p}
	if err != nil || len(p.Entries) == 0 {
		return st, err
	}
	on, err := g.TagsAt(commit)
	if err != nil {
		return st, err
	}
	if st.OnCommit = Newest(on); st.OnCommit != "" {
		return st, nil
	}
	later, err := g.TagsContaining(commit)
	if err != nil {
		return st, err
	}
	sort.Slice(later, func(i, j int) bool { return semver.Compare(later[i], later[j]) < 0 })
	for _, t := range later {
		if c, err := g.changelogAt(t); err == nil && carries(c, p.Entries) {
			st.CarriedBy = t
			return st, nil
		}
	}
	st.TagCommit = g.TagCommit(p.Version)
	return st, nil
}

// MergeFacts are Decide's facts for a merge commit, without Gate and
// Missing: the version its entries get, or the tag it already carries.
func MergeFacts(g Git, commit string, attempt int) (Facts, error) {
	st, err := MergeAt(g, commit)
	f := Facts{Mode: ModeMerge, Commit: commit, Attempt: attempt, Version: st.Version, Since: st.Since,
		TagCommit: st.TagCommit, CarriedBy: st.CarriedBy}
	if st.OnCommit != "" {
		f.Version, f.TagCommit = st.OnCommit, commit
	}
	return f, err
}

// carries says c has every one of entries, word for word.
func carries(c Changelog, entries []Entry) bool {
	have := map[string]int{}
	for _, e := range c.Entries() {
		have[e.key()]++
	}
	for _, e := range entries {
		if have[e.key()]--; have[e.key()] < 0 {
			return false
		}
	}
	return true
}

// NotesAt are the notes of a release on commit: the entries its
// CHANGELOG.md adds over the newest v* tag reachable from its first parent
// (PendingAt). ok is false when it adds none.
func NotesAt(g Git, commit string) (notes string, ok bool) {
	if commit == "" {
		return "", false
	}
	p, err := PendingAt(g, commit)
	if err != nil || len(p.Entries) == 0 {
		return "", false
	}
	return p.Notes(), true
}

// Claim is what the tag job did: the release's tag, and whether this run
// builds and publishes it.
type Claim struct {
	Tag     string
	Release bool
}

// ClaimTag tags commit with the version MergeAt computes once remote's tags
// are fetched, and pushes the tag. When another run's tag took that name
// meanwhile, it computes again with that tag. A tag another run put on
// commit (this being attempt 1), or one on a later commit that carries
// commit's entries, leaves the release to that run.
func ClaimTag(g Git, commit, remote string, attempt int, log io.Writer) (Claim, error) {
	say := func(format string, args ...any) { fmt.Fprintf(log, format+"\n", args...) }
	for {
		if err := g.FetchTags(remote); err != nil {
			return Claim{}, err
		}
		st, err := MergeAt(g, commit)
		switch {
		case err != nil:
			return Claim{}, err
		case len(st.Entries) == 0:
			say("%s adds no entry over %s: nothing to tag", short(commit), tagOrNone(st.Since))
			return Claim{}, nil
		case st.OnCommit != "" && attempt > 1:
			say("%s is tagged %s: this attempt finishes its release", short(commit), st.OnCommit)
			return Claim{Tag: st.OnCommit, Release: true}, nil
		case st.OnCommit != "":
			say("%s is tagged %s by another Release run, which releases it", short(commit), st.OnCommit)
			return Claim{Tag: st.OnCommit}, nil
		case st.CarriedBy != "":
			say("the entries of %s are released in %s, on a later commit: nothing to tag", short(commit), st.CarriedBy)
			return Claim{}, nil
		case st.TagCommit != "":
			return Claim{}, fmt.Errorf("tag %s is on %s, which neither is %s nor carries its entries", st.Version, short(st.TagCommit), short(commit))
		}
		created, err := g.CreateTag(st.Version, commit, remote)
		var elsewhere *TagElsewhereError
		if errors.As(err, &elsewhere) {
			say("%s; computing the version again with that tag", err)
			continue
		}
		if err != nil {
			return Claim{}, err
		}
		if created {
			say("tagged %s on %s: %s over %s", st.Version, short(commit), entryCount(len(st.Entries)), tagOrNone(st.Since))
		} else {
			say("%s was tagged %s by another run meanwhile", short(commit), st.Version)
		}
		return Claim{Tag: st.Version, Release: created || attempt > 1}, nil
	}
}

func entryCount(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

// PublishedMissing is what tag's published release lacks. A draft, which a
// read-only token does not see, counts as nothing published.
func PublishedMissing(ctx context.Context, api *Client, tag string) (Missing, error) {
	rel, err := api.PublishedRelease(ctx, tag)
	if err != nil || rel == nil {
		return Missing{LinuxWindows: true, Darwin: true}, err
	}
	assets, err := api.Assets(ctx, rel.ID)
	if err != nil {
		return Missing{}, err
	}
	var names []string
	sums := map[string]string{}
	for _, a := range assets {
		if a.State != "uploaded" {
			continue
		}
		names = append(names, a.Name)
		if a.Name == SumsName {
			data, err := api.DownloadAsset(ctx, a.ID)
			if err != nil {
				return Missing{}, err
			}
			sums = ParseSums(string(data))
		}
	}
	return MissingFrom(tag, names, sums), nil
}

// The workflows, by file, whose runs on a merge commit decide its release.
const (
	CIWorkflow    = "ci.yml"
	MacOSWorkflow = "macos.yml"
)

// ReleaseGate is GateRelease on the newest push runs of CI and macOS on
// commit, as they are now: CI's latest attempt, every attempt of macOS.
func ReleaseGate(ctx context.Context, api *Client, commit string) (Verdict, []string, error) {
	ci, err := runJobs(ctx, api, CIWorkflow, commit, false)
	if err != nil {
		return Block, nil, err
	}
	mac, err := runJobs(ctx, api, MacOSWorkflow, commit, true)
	if err != nil {
		return Block, nil, err
	}
	v, why := GateRelease(ci, mac)
	return v, why, nil
}

func runJobs(ctx context.Context, api *Client, workflow, commit string, everyAttempt bool) (RunJobs, error) {
	run, err := api.RunOn(ctx, workflow, commit)
	if err != nil || run.ID == 0 {
		return RunJobs{}, err
	}
	rj := RunJobs{ID: run.ID, Status: run.Status}
	latest := max(run.RunAttempt, 1)
	first := latest
	if everyAttempt {
		first = 1
	}
	for a := first; a <= latest; a++ {
		jobs, err := api.AttemptJobs(ctx, run.ID, a)
		if err != nil {
			return RunJobs{}, err
		}
		rj.Attempts = append(rj.Attempts, jobs)
	}
	return rj, nil
}

// Retry reruns the failed jobs of a completed run attempt when DecideRetry
// says so. At GitHub's bound (a run is rerun at most 50 times, within 30
// days) the rerun is refused, and the error says a person starts a new run.
func Retry(ctx context.Context, api *Client, runID int64, attempt int, out io.Writer) error {
	run, err := api.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	in := RetryInput{Run: run, Attempt: attempt}
	if run.Event == "pull_request" {
		if in.Newest, err = api.NewestRun(ctx, run.WorkflowID, run.HeadBranch, run.Event); err != nil {
			return err
		}
	}
	if in.Jobs, err = api.AttemptJobs(ctx, runID, attempt); err != nil {
		return err
	}
	rerun, why := DecideRetry(in)
	if !rerun {
		fmt.Fprintf(out, "run %d attempt %d is not rerun: %s\n", runID, attempt, why)
		return nil
	}
	if err := api.RerunFailedJobs(ctx, runID); err != nil {
		return errors.Join(err, fmt.Errorf("run %d was not rerun (%s): GitHub reruns a run at most 50 times, within 30 days of its start; past that a person starts a new run", runID, why))
	}
	fmt.Fprintf(out, "run %d attempt %d: %s: its failed jobs run again\n", runID, attempt, why)
	return nil
}
