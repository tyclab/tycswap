// updates.go never runs go install, claude update or release requests itself (A26: consumer-defined seams only).

package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// UpdatesView is everything the host knows about pending updates. Available
// is the one answer the page keys on: anything to update. CheckedAt is when
// a check last reached the network (the release endpoint or Claude Code's
// own source); before the first such check it is absent and the page says it
// has not checked yet. The Error fields say what the last check could not
// read, so the page never calls something up to date that it could not
// check; each is optional.
type UpdatesView struct {
	Available  bool                  `json:"available"` // anything to update
	Checking   bool                  `json:"checking"`  // a check is running
	CheckedAt  *time.Time            `json:"checkedAt,omitempty"`
	App        *AppUpdateView        `json:"app,omitempty"`
	ClaudeCode *ClaudeCodeUpdateView `json:"claudeCode,omitempty"`
}

type AppUpdateView struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	Installed bool   `json:"installed,omitempty"`
	Error     string `json:"error,omitempty"`
	Hint      string `json:"hint,omitempty"`
}

type ClaudeCodeUpdateView struct {
	Installed string `json:"installed,omitempty"`
	Latest    string `json:"latest,omitempty"`
	State     string `json:"state"`
	Method    string `json:"method,omitempty"`  // how it is installed, for a person
	Command   string `json:"command,omitempty"` // what an update (or install) runs, for the confirmation
	Detail    string `json:"detail,omitempty"`  // one sentence for the states that need it
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type UpdateResult struct {
	Message string `json:"message"`
	Output  string `json:"output,omitempty"`
}

type UpdatesFacade interface {
	View() UpdatesView
	Check() error
	Apply(target string) (UpdateResult, error)
}

var updateTargets = []string{"app", "claude-code"}

// Six hours: the CLI notice cache lasts a day, but Claude Code releases more often.
const defaultUpdateInterval = 6 * time.Hour

// applyWriteDeadline bounds how long one apply response may take. The server
// sets no WriteTimeout today; pinning a per-request deadline keeps a long
// `go install` or Claude Code update from being cut off should one ever be
// added for the other routes, and still frees the connection of an apply
// that never returns.
const applyWriteDeadline = 30 * time.Minute

func (s *Server) updatesView() *UpdatesView {
	v := s.d.Updates.View()
	return &v
}

// Refresh pushes a fresh state document to every dashboard now instead of at
// the next poll tick; the host calls it whenever its update state changes.
// Safe from any goroutine, before Serve (the request waits for the loop's
// first turn) and after it (then a no-op). It only signals the serve loop,
// which does the broadcast, so a burst of calls coalesces into one.
func (s *Server) Refresh() {
	select {
	case s.refresh <- struct{}{}:
	default: // one is already pending; it will carry this change too
	}
}

func (s *Server) checkUpdates() {
	if s.d.Updates == nil {
		return
	}
	if err := s.d.Updates.Check(); err != nil {
		s.d.Logger("web: updates check: " + err.Error())
	}
}

func (s *Server) handleUpdatesCheck(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Updates != nil, "updates") {
		return
	}
	err := s.d.Updates.Check()
	s.broadcast()
	if err != nil {
		s.d.Logger("web: updates check: " + err.Error())
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{})
}

// handleUpdatesApply runs one update and answers with its UpdateResult. The
// page has already asked the user. Applies run one at a time: two updates at
// once would race on the same files (the binary being replaced, Claude
// Code's install), so a second request while one runs is a 409 rather than
// a wait the browser cannot see. It does not take mutMu: account switches
// must not stall behind a multi-minute install. A failed apply answers
// {"error", "output"}: the output of what ran is what tells the user why,
// and the page's Output disclosure shows it after a failure too.
func (s *Server) handleUpdatesApply(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Updates != nil, "updates") {
		return
	}
	var b struct {
		Target string `json:"target"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	target := strings.TrimSpace(b.Target)
	if target == "" {
		writeError(w, http.StatusBadRequest, "target is required (app or claude-code)")
		return
	}
	known := false
	for _, t := range updateTargets {
		if t == target {
			known = true
		}
	}
	if !known {
		writeError(w, http.StatusBadRequest, "unknown update target "+strconv.Quote(target)+" (want app or claude-code)")
		return
	}
	if !s.applying.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "another update is still running; try again when it has finished")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(applyWriteDeadline))
	res, err := s.d.Updates.Apply(target)
	s.applying.Store(false)
	s.broadcast()
	if err != nil {
		status := statusFor(err)
		// Target and status only: installer output is for the page, not the log.
		s.d.Logger("web: update " + target + " failed (HTTP " + strconv.Itoa(status) + ")")
		body := map[string]any{"error": err.Error()}
		if res.Output != "" {
			body["output"] = res.Output
		}
		writeJSON(w, status, body)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
