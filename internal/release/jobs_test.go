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

func TestGateCI(t *testing.T) {
	passed := recordedJobs(t, "jobs-45598548351-attempt3-passed.json")
	noMac := recordedJobs(t, "jobs-45598548351-attempt1-macos-no-runner.json")
	macFailed := recordedJobs(t, "jobs-45577203103-macos-test-failed.json")
	cancelledRunning := recordedJobs(t, "jobs-45612794530-cancelled-running.json")
	swap := func(jobs []Job, name string, with Job) []Job {
		out := append([]Job{}, jobs...)
		for i := range out {
			if out[i].Name == name {
				with.Name = name
				out[i] = with
			}
		}
		return out
	}
	without := func(jobs []Job, name string) []Job {
		var out []Job
		for _, j := range jobs {
			if j.Name != name {
				out = append(out, j)
			}
		}
		return out
	}
	cases := []struct {
		name string
		jobs []Job
		want Verdict
		why  string
	}{
		{"all passed", passed, Pass, ""},
		{"macOS found no runner: not waited for", noMac, Pass, "macos found no runner"},
		{"macOS ran and failed", macFailed, Block, "macos ended failure"},
		{"cancelled while running", cancelledRunning, Block, "test ended cancelled"},
		{"windows found no runner: wait for its rerun", swap(passed, "windows", findJob(noMac, "macos")), Wait, "windows found no runner"},
		{"test found no runner", swap(passed, "test", findJob(noMac, "macos")), Wait, "test found no runner"},
		{"a failure outweighs a wait", swap(macFailed, "windows", findJob(noMac, "macos")), Block, "macos ended failure"},
		{"a job is missing", without(passed, "windows"), Block, `CI ran no job named "windows"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := GateCI(tc.jobs)
			if got != tc.want || !strings.Contains(strings.Join(why, "; "), tc.why) {
				t.Errorf("GateCI = %v %q, want %v containing %q", got, why, tc.want, tc.why)
			}
		})
	}
}

// macOS ran and failed, then its rerun found no runner: the failure stands.
func TestGateCIAttempts(t *testing.T) {
	failed := recordedJobs(t, "jobs-45577203103-macos-test-failed.json")
	noMac := recordedJobs(t, "jobs-45598548351-attempt1-macos-no-runner.json")
	passed := recordedJobs(t, "jobs-45598548351-attempt3-passed.json")
	cases := []struct {
		name     string
		attempts [][]Job
		want     Verdict
	}{
		{"failed, then no runner", [][]Job{failed, noMac}, Block},
		{"failed, no runner twice", [][]Job{failed, noMac, noMac}, Block},
		{"no runner throughout", [][]Job{noMac, noMac}, Pass},
		{"passed, then no runner", [][]Job{passed, noMac}, Pass},
		{"failed, then passed", [][]Job{failed, passed}, Pass},
	}
	for _, tc := range cases {
		if got, why := GateCIAttempts(tc.attempts); got != tc.want {
			t.Errorf("%s: %v %q, want %v", tc.name, got, why, tc.want)
		}
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
