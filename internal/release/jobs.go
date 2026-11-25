package release

import (
	"fmt"
	"strings"
)

// Job is a workflow job as the REST API lists it.
type Job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	RunnerID   int64  `json:"runner_id"`
	Steps      []Step `json:"steps"`
}

// Step is one step of a Job.
type Step struct {
	Name       string  `json:"name"`
	Conclusion string  `json:"conclusion"`
	StartedAt  *string `json:"started_at"`
}

// Run is a workflow run as the REST API returns it.
type Run struct {
	ID         int64  `json:"id"`
	WorkflowID int64  `json:"workflow_id"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	RunAttempt int    `json:"run_attempt"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
}

// Started says a step of the job has started: a runner took it.
func (j Job) Started() bool {
	for _, s := range j.Steps {
		if s.StartedAt != nil && *s.StartedAt != "" {
			return true
		}
	}
	return false
}

// FoundNoRunner is the runner shortage's signature: the job was cancelled
// before any of its steps started (GitHub gives up on a queued job after
// about 15 minutes, with no runner and no step).
func (j Job) FoundNoRunner() bool { return j.Conclusion == "cancelled" && !j.Started() }

// Verdict is what CI on a merge commit says about releasing it.
type Verdict int

const (
	Pass Verdict = iota
	// Wait: a job the release needs has not finished, or found no runner;
	// a later completion decides.
	Wait
	Block
)

func (v Verdict) String() string {
	return [...]string{"pass", "wait", "block"}[v]
}

// RunJobs is one workflow's run on a merge commit: its status and the jobs
// of its attempts, oldest first, the last being the latest. The zero value
// is a workflow without a run there.
type RunJobs struct {
	ID       int64
	Status   string
	Attempts [][]Job
}

func (r RunJobs) latest() []Job {
	if len(r.Attempts) == 0 {
		return nil
	}
	return r.Attempts[len(r.Attempts)-1]
}

// GateRelease decides on the runs of CI and macOS on a merge commit. CI's
// test and windows must have passed: a job not completed, or one that found
// no runner, waits; anything else blocks. macOS is waited for only while
// its job runs: passed, without a run, queued with no step started, or
// cancelled with none (no runner) releases, unless the last attempt in
// which it ran did not pass (blocks; waits while a rerun is queued); a run
// that ended otherwise blocks. A block outweighs a wait.
func GateRelease(ci, mac RunJobs) (Verdict, []string) {
	verdict, why := Pass, []string(nil)
	worse := func(v Verdict, reason string) {
		if v > verdict {
			verdict = v
		}
		why = append(why, reason)
	}
	if ci.ID == 0 {
		worse(Wait, "CI has no run on this commit yet: its completion decides")
	}
	for _, name := range []string{"test", "windows"} {
		j := findJob(ci.latest(), name)
		switch {
		case ci.ID == 0:
		case j.Name == "" && ci.Status != "completed":
			worse(Wait, name+" has not started yet: CI's completion decides")
		case j.Name == "":
			worse(Block, fmt.Sprintf("CI ran no job named %q", name))
		case j.Conclusion == "success":
		case j.FoundNoRunner():
			worse(Wait, name+" found no runner: the Retry workflow reruns it, and its completion decides again")
		case j.Status != "completed":
			worse(Wait, name+" has not completed: CI's completion decides")
		default:
			worse(Block, fmt.Sprintf("%s ended %s", name, orNone(j.Conclusion)))
		}
	}
	gateMacOS(mac, worse)
	return verdict, why
}

func gateMacOS(mac RunJobs, worse func(Verdict, string)) {
	if mac.ID == 0 {
		worse(Pass, "macOS has no run on this commit: the release does not wait for it")
		return
	}
	j := findJob(mac.latest(), "macos")
	queued := (j.Name == "" && mac.Status != "completed") || (j.Name != "" && j.Status != "completed" && !j.Started())
	switch {
	case j.Conclusion == "success":
		return
	case j.Name != "" && j.Status != "completed" && j.Started():
		worse(Wait, "macos is running: the macOS workflow's completion decides")
		return
	case !queued && !j.FoundNoRunner() && j.Name == "":
		worse(Block, `the macOS workflow ran no job named "macos"`)
		return
	case !queued && !j.FoundNoRunner():
		worse(Block, "macos ended "+orNone(j.Conclusion))
		return
	}
	// macos has not run in the latest attempt: the last attempt in which it
	// ran says whether it failed.
	for i := len(mac.Attempts) - 2; i >= 0; i-- {
		m := findJob(mac.Attempts[i], "macos")
		if m.Name == "" || m.FoundNoRunner() {
			continue
		}
		switch {
		case m.Conclusion == "success":
		case queued:
			worse(Wait, fmt.Sprintf("macos ran in attempt %d and ended %s, and its rerun is queued: the rerun's completion decides", i+1, orNone(m.Conclusion)))
			return
		default:
			worse(Block, fmt.Sprintf("macos ran in attempt %d and ended %s; its rerun found no runner", i+1, orNone(m.Conclusion)))
			return
		}
		break
	}
	if queued {
		worse(Pass, "macos is queued and has not started: the release does not wait for it")
	} else {
		worse(Pass, "macos found no runner: the release does not wait for it")
	}
}

func findJob(jobs []Job, name string) Job {
	for _, j := range jobs {
		if j.Name == name {
			return j
		}
	}
	return Job{}
}

// RetryInput is what DecideRetry reads about one completed run attempt.
type RetryInput struct {
	Run     Run
	Attempt int // the attempt whose completion is being handled
	// Newest is the newest run of the workflow on a pull request's branch;
	// only pull request runs are replaced by newer pushes.
	Newest int64
	Jobs   []Job // the attempt's jobs
}

// DecideRetry says whether to rerun a run's failed jobs: only when every job
// that did not succeed found no runner (a skipped dependant aside), and the
// attempt is the run's latest and not superseded. A job that ran and failed
// leaves the run to a person, who reruns the cancelled jobs with it.
func DecideRetry(in RetryInput) (bool, string) {
	r := in.Run
	switch {
	case r.Status != "completed":
		return false, fmt.Sprintf("run %d is %s", r.ID, r.Status)
	case r.RunAttempt > in.Attempt:
		return false, fmt.Sprintf("run %d attempt %d exists; this was attempt %d", r.ID, r.RunAttempt, in.Attempt)
	case r.Event == "pull_request" && in.Newest != r.ID:
		return false, fmt.Sprintf("run %d is superseded by run %d on %s", r.ID, in.Newest, r.HeadBranch)
	}
	var noRunner []string
	for _, j := range in.Jobs {
		switch {
		case j.Conclusion == "success" || j.Conclusion == "skipped" || j.Conclusion == "neutral":
		case j.FoundNoRunner():
			noRunner = append(noRunner, j.Name)
		default:
			return false, fmt.Sprintf("%s ran and ended %s: a person decides on this run", j.Name, orNone(j.Conclusion))
		}
	}
	if len(noRunner) == 0 {
		return false, "no job found no runner"
	}
	return true, strings.Join(noRunner, ", ") + " found no runner"
}

func orNone(s string) string {
	if s == "" {
		return "without a conclusion"
	}
	return s
}
