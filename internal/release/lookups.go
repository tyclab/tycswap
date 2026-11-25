package release

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// VersionAt is the version a merge commit's CHANGELOG.md names against its
// first parent's, counting the tags merged into that parent: the pull
// request's check with the base the merge had. A tag a later merge got
// first does not make this one late.
func VersionAt(g Git, commit string) (string, error) {
	head, err := g.Changelog(commit)
	if err != nil {
		return "", err
	}
	var base string
	var tags []string
	if parent := g.Commit(commit + "^1"); parent != "" {
		if base, err = g.Changelog(parent); err != nil {
			return "", err
		}
		if tags, err = g.Tags(parent); err != nil {
			return "", err
		}
	}
	return CheckVersion(head, base, tags)
}

// NotesAt is tag's section in commit's CHANGELOG.md; ok is false when there
// is none.
func NotesAt(g Git, commit, tag string) (notes string, ok bool) {
	text, err := g.Changelog(commit)
	if err != nil || commit == "" {
		return "", false
	}
	c, err := Parse(text)
	if err != nil {
		return "", false
	}
	return c.Notes(tag)
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

// CIGate is GateCIAttempts on CI's run up to the attempt that completed;
// the earlier attempts are read only when macos found no runner.
func CIGate(ctx context.Context, api *Client, run int64, attempt int) (Verdict, []string, error) {
	jobs, err := api.AttemptJobs(ctx, run, attempt)
	if err != nil {
		return Block, nil, err
	}
	attempts := [][]Job{jobs}
	if findJob(jobs, "macos").FoundNoRunner() {
		for a := attempt - 1; a >= 1; a-- {
			earlier, err := api.AttemptJobs(ctx, run, a)
			if err != nil {
				return Block, nil, err
			}
			attempts = append([][]Job{earlier}, attempts...)
		}
	}
	v, why := GateCIAttempts(attempts)
	return v, why, nil
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
