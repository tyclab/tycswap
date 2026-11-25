package release

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A merge commit releases the entries it has over the newest tag reachable
// from its first parent; a tag on a later commit is not counted. The merge
// before A121 (v0.24.1, which named its version) computes the version it
// named.
func TestPendingAtAndNotesAt(t *testing.T) {
	_, a, _, commits := gitHistory(t, v0240, v0241, since("- Every merge releases."), since("- Every merge releases."))
	mustTag := func(tag, commit string) {
		t.Helper()
		if _, err := a.run("tag", tag, commit); err != nil {
			t.Fatal(err)
		}
	}
	mustTag("v0.24.0", commits[0])
	mustTag("v0.30.0", commits[2])
	cases := []struct {
		commit         int
		since, version string
		entries        string
	}{
		{1, "v0.24.0", "v0.24.1", "- **Fixed:** start login."},
		{2, "v0.24.0", "v0.25.0", "- Every merge releases.|- **Fixed:** start login."},
	}
	for _, tc := range cases {
		p, err := PendingAt(a, commits[tc.commit])
		if err != nil || p.Since != tc.since || p.Version != tc.version || texts(p.Entries) != tc.entries {
			t.Errorf("PendingAt(%d) = %+v, %v", tc.commit, p, err)
		}
	}
	mustTag("v0.24.1", commits[1])
	if p, err := PendingAt(a, commits[2]); err != nil || p.Version != "v0.25.0" || texts(p.Entries) != "- Every merge releases." {
		t.Errorf("over v0.24.1: %+v, %v", p, err)
	}
	if p, err := PendingAt(a, commits[3]); err != nil || p.Since != "v0.30.0" || len(p.Entries) != 0 {
		t.Errorf("a merge without an entry: %+v, %v", p, err)
	}
	if notes, ok := NotesAt(a, commits[2]); !ok || notes != "- Every merge releases." {
		t.Errorf("NotesAt = %q, %v", notes, ok)
	}
	if notes, ok := NotesAt(a, commits[1]); !ok || notes != "- **Fixed:** start login." {
		t.Errorf("NotesAt for v0.24.1 = %q, %v", notes, ok)
	}
	if _, ok := NotesAt(a, commits[3]); ok {
		t.Error("notes for a commit that adds no entry")
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

// The gate reads the newest push run of each workflow on the commit: CI's
// latest attempt only, macOS's every attempt.
func TestReleaseGateReadsBothWorkflows(t *testing.T) {
	const sha = "abc"
	passed := recordedJobs(t, "jobs-45598548351-attempt3-passed.json")
	noMac := recordedJobs(t, "jobs-45598548351-attempt1-macos-no-runner.json")
	macFailed := recordedJobs(t, "jobs-45577203103-macos-test-failed.json")
	cancelledRunning := recordedJobs(t, "jobs-45612794530-cancelled-running.json")
	setup := func(t *testing.T) (*fakeGitHub, *Client) {
		f, c := newFakeGitHub(t)
		f.pushRuns["ci.yml/"+sha] = []Run{{ID: 9, Status: "completed", RunAttempt: 1}, {ID: 10, Status: "completed", RunAttempt: 2}}
		f.jobs["9/1"] = cancelledRunning
		f.jobs["10/1"] = cancelledRunning
		f.jobs["10/2"] = ciRun("completed", passed).Attempts[0]
		return f, c
	}
	t.Run("macOS ran and failed before its rerun found no runner", func(t *testing.T) {
		f, c := setup(t)
		f.pushRuns["macos.yml/"+sha] = []Run{{ID: 20, Status: "completed", RunAttempt: 2}}
		f.jobs["20/1"] = macRun("completed", macFailed).Attempts[0]
		f.jobs["20/2"] = macRun("completed", noMac).Attempts[0]
		v, why, err := ReleaseGate(context.Background(), c, sha)
		if err != nil || v != Block || !strings.Contains(strings.Join(why, ";"), "macos ran in attempt 1 and ended failure") {
			t.Errorf("ReleaseGate = %v %q %v", v, why, err)
		}
		for _, call := range f.calls {
			if strings.Contains(call, "/runs/10/attempts/1/") || strings.Contains(call, "/runs/9/") {
				t.Errorf("read %s: CI counts its newest run's latest attempt only", call)
			}
		}
	})
	t.Run("macOS has no run", func(t *testing.T) {
		_, c := setup(t)
		v, why, err := ReleaseGate(context.Background(), c, sha)
		if err != nil || v != Pass || !strings.Contains(strings.Join(why, ";"), "macOS has no run") {
			t.Errorf("ReleaseGate = %v %q %v", v, why, err)
		}
	})
	t.Run("CI has no run", func(t *testing.T) {
		f, c := setup(t)
		delete(f.pushRuns, "ci.yml/"+sha)
		if v, why, err := ReleaseGate(context.Background(), c, sha); err != nil || v != Wait {
			t.Errorf("ReleaseGate = %v %q %v", v, why, err)
		}
	})
	t.Run("a workflow GitHub does not know", func(t *testing.T) {
		_, c := setup(t)
		if run, err := c.RunOn(context.Background(), "absent.yml", sha); err != nil || run.ID != 0 {
			t.Errorf("RunOn = %+v, %v", run, err)
		}
	})
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
