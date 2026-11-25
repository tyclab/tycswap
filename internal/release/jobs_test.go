package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordedJobs reads a job listing recorded with
// `gh api repos/tyclab/tycswap/actions/runs/<id>/attempts/<n>/jobs`.
func recordedJobs(t *testing.T, name string) []Job {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var page struct{ Jobs []Job }
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatal(err)
	}
	return page.Jobs
}

func TestFoundNoRunnerOnRecordedJobs(t *testing.T) {
	cases := []struct {
		file, job string
		want      bool
	}{
		// Queued 15 minutes, cancelled by GitHub with zero steps, rerun later.
		{"jobs-45598548351-attempt1-macos-no-runner.json", "macos", true},
		{"jobs-45598548351-attempt1-macos-no-runner.json", "test", false},
		{"jobs-45598548351-attempt3-passed.json", "macos", false},
		// Ran for 11 minutes and failed in `go test`.
		{"jobs-45577203103-macos-test-failed.json", "macos", false},
		// Cancelled while running: its first steps had run.
		{"jobs-45612794530-cancelled-running.json", "macos", false},
		// A newer push cancelled the run while macOS still queued: the same
		// signature, which the retry tells apart by the newer run.
		{"jobs-44173229395-superseded-pr.json", "macos", true},
		{"jobs-44173229395-superseded-pr.json", "windows", false},
		// Queued for a Mac, not yet given up on: no runner and no step, but
		// not cancelled either.
		{"jobs-45694616515-release-darwin-queued.json", "darwin", false},
		{"jobs-45694616515-release-darwin-queued.json", "linux-windows", false},
	}
	for _, tc := range cases {
		j := findJob(recordedJobs(t, tc.file), tc.job)
		if j.Name == "" {
			t.Fatalf("%s has no job %s", tc.file, tc.job)
		}
		if got := j.FoundNoRunner(); got != tc.want {
			t.Errorf("%s %s: FoundNoRunner = %v, want %v", tc.file, tc.job, got, tc.want)
		}
	}
}

// The recorded listings hold CI's jobs as they were before macos had its
// own workflow (DESIGN A121): ciRun and macRun take them apart again.
func ciRun(status string, attempts ...[]Job) RunJobs {
	r := RunJobs{ID: 1, Status: status}
	for _, jobs := range attempts {
		var ci []Job
		for _, j := range jobs {
			if j.Name == "test" || j.Name == "windows" {
				ci = append(ci, j)
			}
		}
		r.Attempts = append(r.Attempts, ci)
	}
	return r
}

func macRun(status string, attempts ...[]Job) RunJobs {
	r := RunJobs{ID: 2, Status: status}
	for _, jobs := range attempts {
		var mac []Job
		if j := findJob(jobs, "macos"); j.Name != "" {
			mac = append(mac, j)
		}
		r.Attempts = append(r.Attempts, mac)
	}
	return r
}

// renamed is jobs with job from a recording standing in for name.
func renamed(jobs []Job, name string, job Job) []Job {
	job.Name = name
	out := []Job{job}
	for _, j := range jobs {
		if j.Name != name {
			out = append(out, j)
		}
	}
	return out
}

func without(jobs []Job, name string) []Job {
	var out []Job
	for _, j := range jobs {
		if j.Name != name {
			out = append(out, j)
		}
	}
	return out
}

func TestGateRelease(t *testing.T) {
	passed := recordedJobs(t, "jobs-45598548351-attempt3-passed.json")
	noMac := recordedJobs(t, "jobs-45598548351-attempt1-macos-no-runner.json")
	macFailed := recordedJobs(t, "jobs-45577203103-macos-test-failed.json")
	cancelledRunning := recordedJobs(t, "jobs-45612794530-cancelled-running.json")
	// Release run 45694616515: darwin queued for a Mac (no runner, no step),
	// linux-windows running on its runner.
	release := recordedJobs(t, "jobs-45694616515-release-darwin-queued.json")
	queued, running := findJob(release, "darwin"), findJob(release, "linux-windows")
	macQueued, macRunning := renamed(passed, "macos", queued), renamed(passed, "macos", running)
	noRunner := findJob(noMac, "macos")
	const done, inProgress = "completed", "in_progress"
	cases := []struct {
		name    string
		ci, mac RunJobs
		want    Verdict
		why     string
	}{
		{"all passed", ciRun(done, passed), macRun(done, passed), Pass, ""},
		{"CI has no run yet", RunJobs{}, macRun(done, passed), Wait, "CI has no run on this commit yet"},
		{"windows still running: macOS completed first", ciRun(inProgress, renamed(passed, "windows", running)), macRun(done, passed), Wait, "windows has not completed: CI's completion decides"},
		{"windows found no runner", ciRun(done, renamed(passed, "windows", noRunner)), macRun(done, passed), Wait, "windows found no runner"},
		{"test found no runner", ciRun(done, renamed(passed, "test", noRunner)), macRun(done, passed), Wait, "test found no runner"},
		{"CI cancelled while running", ciRun(done, cancelledRunning), macRun(done, passed), Block, "test ended cancelled"},
		{"CI ran no windows", ciRun(done, without(passed, "windows")), macRun(done, passed), Block, `CI ran no job named "windows"`},
		{"CI has not started windows", ciRun("queued", without(passed, "windows")), macRun(done, passed), Wait, "windows has not started yet"},
		{"macOS has no run", ciRun(done, passed), RunJobs{}, Pass, "macOS has no run on this commit: the release does not wait for it"},
		{"macOS found no runner", ciRun(done, passed), macRun(done, noMac), Pass, "macos found no runner: the release does not wait for it"},
		{"macOS queued with no step started", ciRun(done, passed), macRun("queued", macQueued), Pass, "macos is queued and has not started: the release does not wait for it"},
		{"macOS run without its job yet", ciRun(done, passed), macRun("queued", nil), Pass, "macos is queued"},
		{"macOS running", ciRun(done, passed), macRun(inProgress, macRunning), Wait, "macos is running: the macOS workflow's completion decides"},
		{"macOS ran and failed", ciRun(done, passed), macRun(done, macFailed), Block, "macos ended failure"},
		{"macOS cancelled while running", ciRun(done, passed), macRun(done, cancelledRunning), Block, "macos ended cancelled"},
		{"macOS workflow without the job", ciRun(done, passed), macRun(done, nil), Block, `the macOS workflow ran no job named "macos"`},
		{"macOS failed, then no runner", ciRun(done, passed), macRun(done, macFailed, noMac), Block, "macos ran in attempt 1 and ended failure; its rerun found no runner"},
		{"macOS failed, no runner twice", ciRun(done, passed), macRun(done, macFailed, noMac, noMac), Block, "macos ran in attempt 1 and ended failure"},
		{"macOS no runner throughout", ciRun(done, passed), macRun(done, noMac, noMac), Pass, "macos found no runner"},
		{"macOS passed, then no runner", ciRun(done, passed), macRun(done, passed, noMac), Pass, "macos found no runner"},
		{"macOS failed, then passed", ciRun(done, passed), macRun(done, macFailed, passed), Pass, ""},
		{"macOS failed, its rerun queued", ciRun(done, passed), macRun("queued", macFailed, macQueued), Wait, "macos ran in attempt 1 and ended failure, and its rerun is queued: the rerun's completion decides"},
		{"macOS passed, its rerun queued", ciRun(done, passed), macRun("queued", passed, macQueued), Pass, "macos is queued"},
		{"macOS failed while CI still runs: a failure outweighs a wait", ciRun(inProgress, renamed(passed, "windows", running)), macRun(done, macFailed), Block, "macos ended failure"},
		{"CI passed while macOS runs", ciRun(done, passed), macRun(inProgress, macRunning), Wait, "macos is running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := GateRelease(tc.ci, tc.mac)
			if got != tc.want || !strings.Contains(strings.Join(why, "; "), tc.why) {
				t.Errorf("GateRelease = %v %q, want %v containing %q", got, why, tc.want, tc.why)
			}
		})
	}
}

func TestDecideRetry(t *testing.T) {
	noMac := recordedJobs(t, "jobs-45598548351-attempt1-macos-no-runner.json")
	passed := recordedJobs(t, "jobs-45598548351-attempt3-passed.json")
	macFailed := recordedJobs(t, "jobs-45577203103-macos-test-failed.json")
	superseded := recordedJobs(t, "jobs-44173229395-superseded-pr.json")
	cancelledRunning := recordedJobs(t, "jobs-45612794530-cancelled-running.json")
	onlyMacNoRunner := append([]Job{}, superseded...)
	for i := range onlyMacNoRunner {
		if onlyMacNoRunner[i].Name == "windows" {
			onlyMacNoRunner[i] = findJob(passed, "windows")
		}
	}
	main := Run{ID: 45598548351, Event: "push", Status: "completed", RunAttempt: 1}
	pr := Run{ID: 44173229395, Event: "pull_request", Status: "completed", RunAttempt: 1, HeadBranch: "feat/x"}
	cases := []struct {
		name   string
		in     RetryInput
		rerun  bool
		reason string
	}{
		{"main: macOS found no runner", RetryInput{Run: main, Attempt: 1, Jobs: noMac}, true, "macos found no runner"},
		{"nothing failed", RetryInput{Run: main, Attempt: 1, Jobs: passed}, false, "no job found no runner"},
		{"macOS ran and failed", RetryInput{Run: main, Attempt: 1, Jobs: macFailed}, false, "macos ran and ended failure"},
		{"cancelled while running", RetryInput{Run: main, Attempt: 1, Jobs: cancelledRunning}, false, "ran and ended cancelled"},
		{"a person reran it already", RetryInput{Run: Run{ID: main.ID, Event: "push", Status: "completed", RunAttempt: 2}, Attempt: 1, Jobs: noMac}, false, "attempt 2 exists"},
		{"still running", RetryInput{Run: Run{ID: main.ID, Event: "push", Status: "in_progress", RunAttempt: 1}, Attempt: 1, Jobs: noMac}, false, "in_progress"},
		{"pull request, newest run", RetryInput{Run: pr, Attempt: 1, Newest: pr.ID, Jobs: onlyMacNoRunner}, true, "macos found no runner"},
		{"pull request superseded by a newer push", RetryInput{Run: pr, Attempt: 1, Newest: pr.ID + 7, Jobs: onlyMacNoRunner}, false, "superseded"},
		{"pull request with a failure beside", RetryInput{Run: pr, Attempt: 1, Newest: pr.ID, Jobs: superseded}, false, "windows ran and ended failure"},
		{"release run: a skipped dependant is no failure", RetryInput{Run: Run{ID: 9, Event: "workflow_run", Status: "completed", RunAttempt: 1}, Attempt: 1,
			Jobs: append(noMac, Job{Name: "publish", Status: "completed", Conclusion: "skipped"})}, true, "macos found no runner"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rerun, reason := DecideRetry(tc.in)
			if rerun != tc.rerun || !strings.Contains(reason, tc.reason) {
				t.Errorf("DecideRetry = %v %q, want %v containing %q", rerun, reason, tc.rerun, tc.reason)
			}
		})
	}
}
