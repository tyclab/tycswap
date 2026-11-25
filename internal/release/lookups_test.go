package release

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersionAtAndNotesAt(t *testing.T) {
	_, a, _, commits := gitRepos(t)
	if _, err := a.run("tag", "v0.23.3", commits[1]); err != nil {
		t.Fatal(err)
	}
	// A tag on a later commit, not merged into the parent, is not counted.
	if _, err := a.run("tag", "v0.30.0", commits[2]); err != nil {
		t.Fatal(err)
	}
	if v, err := VersionAt(a, commits[2]); err != nil || v != "v0.24.0" {
		t.Errorf("VersionAt = %q, %v", v, err)
	}
	if v, err := VersionAt(a, commits[1]); err != nil || v != "" {
		t.Errorf("the commit with the old format: %q, %v", v, err)
	}
	if notes, ok := NotesAt(a, commits[2], "v0.24.0"); !ok || notes != "- Releases publish themselves." {
		t.Errorf("NotesAt = %q, %v", notes, ok)
	}
	if _, ok := NotesAt(a, commits[1], "v0.24.0"); ok {
		t.Error("notes at a commit without the section")
	}
}

func TestPublishedMissing(t *testing.T) {
	f, c := newFakeGitHub(t)
	ctx := context.Background()
	const tag = "v0.24.0"
	if m, err := PublishedMissing(ctx, c, tag); err != nil || !m.LinuxWindows || !m.Darwin {
		t.Errorf("no release: %+v %v", m, err)
	}
	f.addRelease(tag, true, "a draft is not seen")
	if m, _ := PublishedMissing(ctx, c, tag); !m.LinuxWindows || !m.Darwin {
		t.Errorf("draft: %+v", m)
	}
	if err := publisher(c, tag, linuxWindowsDist(t, tag, " bytes"), true).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if m, err := PublishedMissing(ctx, c, tag); err != nil || m.LinuxWindows || !m.Darwin {
		t.Errorf("published without macOS: %+v %v", m, err)
	}
}

func TestCIGateReadsTheCompletedAttempt(t *testing.T) {
	f, c := newFakeGitHub(t)
	f.jobs["45598548351/1"] = recordedJobs(t, "jobs-45598548351-attempt1-macos-no-runner.json")
	f.jobs["45598548351/2"] = nil // the rerun's attempt, still running
	v, why, err := CIGate(context.Background(), c, 45598548351, 1)
	if err != nil || v != Pass || !strings.Contains(strings.Join(why, ";"), "macos found no runner") {
		t.Errorf("CIGate = %v %q %v", v, why, err)
	}
	f.jobs["7/1"] = recordedJobs(t, "jobs-45577203103-macos-test-failed.json")
	f.jobs["7/2"] = recordedJobs(t, "jobs-45598548351-attempt1-macos-no-runner.json")
	if v, why, err := CIGate(context.Background(), c, 7, 2); err != nil || v != Block || !strings.Contains(strings.Join(why, ";"), "macos ran in attempt 1 and ended failure") {
		t.Errorf("a failure before the runner-less rerun: %v %q %v", v, why, err)
	}
}

func TestRetry(t *testing.T) {
	const id = 45598548351
	setup := func(t *testing.T, run Run, jobsFile string) (*fakeGitHub, *Client) {
		f, c := newFakeGitHub(t)
		f.runs[id] = run
		f.jobs["45598548351/1"] = recordedJobs(t, jobsFile)
		return f, c
	}
	t.Run("reruns a job that found no runner", func(t *testing.T) {
		f, c := setup(t, Run{ID: id, WorkflowID: 7, Event: "push", Status: "completed", RunAttempt: 1}, "jobs-45598548351-attempt1-macos-no-runner.json")
		var out bytes.Buffer
		if err := Retry(context.Background(), c, id, 1, &out); err != nil || len(f.reruns) != 1 {
			t.Errorf("reruns %v, err %v, out %q", f.reruns, err, out.String())
		}
	})
	t.Run("leaves a failed test to a person", func(t *testing.T) {
		f, c := setup(t, Run{ID: id, WorkflowID: 7, Event: "push", Status: "completed", RunAttempt: 1}, "jobs-45577203103-macos-test-failed.json")
		var out bytes.Buffer
		if err := Retry(context.Background(), c, id, 1, &out); err != nil || len(f.reruns) != 0 || !strings.Contains(out.String(), "macos ran and ended failure") {
			t.Errorf("reruns %v, err %v, out %q", f.reruns, err, out.String())
		}
	})
	t.Run("a superseded pull request run", func(t *testing.T) {
		f, c := setup(t, Run{ID: id, WorkflowID: 7, Event: "pull_request", Status: "completed", RunAttempt: 1, HeadBranch: "feat/x"}, "jobs-45598548351-attempt1-macos-no-runner.json")
		f.newest["7/feat/x/pull_request"] = id + 100
		var out bytes.Buffer
		if err := Retry(context.Background(), c, id, 1, &out); err != nil || len(f.reruns) != 0 || !strings.Contains(out.String(), "superseded") {
			t.Errorf("reruns %v, err %v, out %q", f.reruns, err, out.String())
		}
	})
	t.Run("the newest pull request run", func(t *testing.T) {
		f, c := setup(t, Run{ID: id, WorkflowID: 7, Event: "pull_request", Status: "completed", RunAttempt: 1, HeadBranch: "feat/x"}, "jobs-45598548351-attempt1-macos-no-runner.json")
		f.newest["7/feat/x/pull_request"] = id
		if err := Retry(context.Background(), c, id, 1, &bytes.Buffer{}); err != nil || len(f.reruns) != 1 {
			t.Errorf("reruns %v, err %v", f.reruns, err)
		}
	})
	t.Run("GitHub's bound refuses the rerun", func(t *testing.T) {
		f, c := setup(t, Run{ID: id, WorkflowID: 7, Event: "push", Status: "completed", RunAttempt: 1}, "jobs-45598548351-attempt1-macos-no-runner.json")
		f.rerunOK = false
		err := Retry(context.Background(), c, id, 1, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "at most 50 times") || !strings.Contains(err.Error(), "HTTP 403") {
			t.Errorf("error %v", err)
		}
	})
}
