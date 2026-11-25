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

// FoundNoRunner is the runner shortage's signature: the job was cancelled
// before any of its steps started (GitHub gives up on a queued job after
// about 15 minutes, with no runner and no step).
func (j Job) FoundNoRunner() bool {
	if j.Conclusion != "cancelled" {
		return false
	}
	for _, s := range j.Steps {
		if s.StartedAt != nil && *s.StartedAt != "" {
			return false
		}
	}
	return true
}

// Verdict is what CI on a merge commit says about releasing it.
type Verdict int

const (
	Pass Verdict = iota
	// Wait: a required job found no runner; its rerun decides.
	Wait
	Block
)

func (v Verdict) String() string {
	return [...]string{"pass", "wait", "block"}[v]
}

// GateCI decides on CI's jobs of the merge commit: test and windows must
// have passed; macos must have passed or found no runner (it is not waited
// for); anything else blocks the release.
func GateCI(jobs []Job) (Verdict, []string) {
	byName := map[string]Job{}
	for _, j := range jobs {
		byName[j.Name] = j
	}
	verdict, why := Pass, []string(nil)
	worse := func(v Verdict, reason string) {
		if v > verdict {
			verdict = v
		}
		why = append(why, reason)
	}
	for _, name := range []string{"test", "windows", "macos"} {
		j, ok := byName[name]
		switch {
		case !ok:
			worse(Block, fmt.Sprintf("CI ran no job named %q", name))
		case j.Conclusion == "success":
		case j.FoundNoRunner() && name == "macos":
			worse(Pass, "macos found no runner: the release does not wait for it")
		case j.FoundNoRunner():
			worse(Wait, name+" found no runner: the Retry workflow reruns it, and CI's next completion decides again")
		default:
			worse(Block, fmt.Sprintf("%s ended %s", name, orNone(j.Conclusion)))
		}
	}
	return verdict, why
}

// GateCIAttempts is GateCI on the latest of a run's attempts (oldest
// first), except that a macos that found no runner blocks when the last
// earlier attempt in which macos ran did not pass: it ran and failed.
func GateCIAttempts(attempts [][]Job) (Verdict, []string) {
	latest := attempts[len(attempts)-1]
	v, why := GateCI(latest)
	if v != Pass || !findJob(latest, "macos").FoundNoRunner() {
		return v, why
	}
	for i := len(attempts) - 2; i >= 0; i-- {
		m := findJob(attempts[i], "macos")
		if m.Name == "" || m.FoundNoRunner() {
			continue
		}
		if m.Conclusion != "success" {
			return Block, append(why, fmt.Sprintf("macos ran in attempt %d and ended %s", i+1, orNone(m.Conclusion)))
		}
		break
	}
	return v, why
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
