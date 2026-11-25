package release

import (
	"fmt"
	"io"
	"strings"
)

// Mode is what started the Release workflow.
type Mode string

const (
	// ModeMerge: CI completed on a push to main; CHANGELOG.md decides.
	ModeMerge Mode = "merge"
	// ModeTag: a person pushed a v* tag; it becomes a draft, as before.
	ModeTag Mode = "tag"
	// ModeDispatch: a person asked for an existing tag's release by hand.
	ModeDispatch Mode = "dispatch"
)

// Facts are what Decide reads; Gate and Missing are asked only when needed.
type Facts struct {
	Mode   Mode
	Commit string // the merge commit, or the tag's commit
	// Version is the release: CheckVersion's answer on the merge commit, or
	// the tag pushed or named.
	Version string
	// TagCommit is the commit tag Version points at, "" when there is none.
	TagCommit string
	// Attempt is this Release run's attempt: 1 for a fresh run.
	Attempt  int
	HasNotes bool // the tag's commit's CHANGELOG.md has Version's section
	Gate     func() (Verdict, []string, error)
	Missing  func() (Missing, error)
}

// Missing says which builds a published release lacks, or lacks sums for.
type Missing struct{ LinuxWindows, Darwin bool }

// MissingFrom reads a published release's file names and its SHA256SUMS.
func MissingFrom(tag string, assets []string, sums map[string]string) Missing {
	have := map[string]bool{}
	for _, a := range assets {
		have[a] = true
	}
	lacks := func(names []string) bool {
		for _, n := range names {
			if !have[n] || sums[n] == "" {
				return true
			}
		}
		return false
	}
	return Missing{LinuxWindows: lacks(LinuxWindowsAssets(tag)), Darwin: lacks(DarwinAssets(tag))}
}

// Plan is what the Release workflow's jobs do; the zero Plan does nothing.
type Plan struct {
	Tag, Commit                              string
	CreateTag, Publish, LinuxWindows, Darwin bool
}

// Write prints the plan as the decide step's outputs.
func (p Plan) Write(w io.Writer) {
	fmt.Fprintf(w, "tag=%s\ncommit=%s\ncreate-tag=%t\npublish=%t\nlinux-windows=%t\ndarwin=%t\n",
		p.Tag, p.Commit, p.CreateTag, p.Publish, p.LinuxWindows, p.Darwin)
}

// Decide plans one Release run. A merge commit is tagged only when CI
// passed on it (GateCI); a later fresh run for a tagged merge commit leaves
// the release to the run that tagged it, and a rerun (attempt > 1) finishes
// what is missing.
func Decide(f Facts, log io.Writer) (Plan, error) {
	say := func(format string, args ...any) { fmt.Fprintf(log, format+"\n", args...) }
	switch {
	case f.Mode == ModeMerge && f.Version == "":
		say("%s names no new version in CHANGELOG.md: nothing to release", short(f.Commit))
		return Plan{}, nil
	case f.Mode == ModeDispatch && f.TagCommit == "":
		return Plan{}, fmt.Errorf("there is no tag %s", f.Version)
	case f.TagCommit != "" && f.TagCommit != f.Commit:
		return Plan{}, fmt.Errorf("tag %s points at %s, not at %s", f.Version, short(f.TagCommit), short(f.Commit))
	}
	plan := Plan{Tag: f.Version, Commit: f.Commit, Publish: f.Mode == ModeMerge || (f.Mode == ModeDispatch && f.HasNotes)}
	if f.Mode == ModeMerge && f.TagCommit == "" {
		verdict, why, err := f.Gate()
		if err != nil {
			return Plan{}, err
		}
		for _, w := range why {
			say("CI on %s: %s", short(f.Commit), w)
		}
		switch verdict {
		case Wait:
			say("%s is not tagged yet", f.Version)
			return Plan{}, nil
		case Block:
			return Plan{}, fmt.Errorf("%s is not released: CI did not pass on %s (%s)", f.Version, short(f.Commit), strings.Join(why, "; "))
		}
		plan.CreateTag, plan.LinuxWindows, plan.Darwin = true, true, true
		say("CI passed on %s: tagging and releasing %s", short(f.Commit), f.Version)
		return plan, nil
	}
	if f.Mode == ModeMerge && f.Attempt <= 1 {
		say("%s is already tagged on %s: the Release run that tagged it finishes it (rerun that run to finish a release that stopped)", f.Version, short(f.Commit))
		return Plan{}, nil
	}
	m, err := f.Missing()
	if err != nil {
		return Plan{}, err
	}
	plan.LinuxWindows, plan.Darwin = m.LinuxWindows, m.Darwin
	if !m.LinuxWindows && !m.Darwin {
		say("release %s is published with every build and its sums: nothing to do", f.Version)
		return Plan{}, nil
	}
	say("release %s: building Linux and Windows %t, macOS %t; publish %t", f.Version, plan.LinuxWindows, plan.Darwin, plan.Publish)
	return plan, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
