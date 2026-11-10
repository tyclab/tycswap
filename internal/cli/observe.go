package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/usage"
)

// observeGap spaces the fetches a session's status line can request. At the
// burn measured on 2026-11-04 (a Pro 5h window from 42% to 100% within 356 s,
// at least 9.8 points a minute) it leaves at most 2.5 points between readings
// (DESIGN A75).
const observeGap = 15 * time.Second

// observedNearBar reports whether the statusline JSON Claude Code passes its
// status line names a five-hour or weekly window within the escalation margin
// of its bar. An absent window (an Ultra account has no five-hour one) or a
// spend limit, which tycswap does not rotate on, never counts.
func observedNearBar(raw []byte, s settings.AutoSwitchSettings) bool {
	var in struct {
		RateLimits map[string]struct {
			UsedPercentage json.RawMessage `json:"used_percentage"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return false
	}
	for window, bar := range map[string]float64{"five_hour": s.FiveHourThreshold, "seven_day": s.SevenDayThreshold} {
		w, ok := in.RateLimits[window]
		if !ok {
			continue
		}
		pct, err := strconv.ParseFloat(string(w.UsedPercentage), 64)
		if err == nil && pct >= bar-usage.EscalationMarginPct {
			return true
		}
	}
	return false
}

// observeTicks records a default-login session's model and reports whether
// the observation runs a tick: near a bar at most once per observeGap, with
// the observed account's poll marked due (DESIGN A75), and at once when the
// session's model is new or changed, on the usage already cached (A77).
// While auto-switch is off it only records the model (A83).
func observeTicks(sw *core.Switcher, raw []byte, s settings.AutoSwitchSettings, enabled bool) bool {
	group := observedGroup(sw.BackupDir())
	modelNews := false
	if group == "" {
		session, model := observedSessionModel(raw)
		news, err := reporting.RecordDefaultSessionModel(sw.BackupDir(), session, model, sw.Clk.Now())
		if err != nil {
			sw.Log.Debugf("Session model not recorded: %v", err)
		}
		modelNews = news
	}
	if !enabled {
		return false
	}
	if observedNearBar(raw, s) && claimObserveSlot(sw.BackupDir(), sw.Clk.Now()) {
		markObservedDue(sw, group)
		return true
	}
	return modelNews
}

// observedSessionModel is the session and the model id the status line reports.
func observedSessionModel(raw []byte) (string, string) {
	var in struct {
		SessionID string `json:"session_id"`
		Model     struct {
			ID string `json:"id"`
		} `json:"model"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", ""
	}
	return in.SessionID, in.Model.ID
}

// claimObserveSlot lets one observation through per observeGap across every
// session's status line; the others return at once.
func claimObserveSlot(dir string, now time.Time) bool {
	lock := filelock.New(filepath.Join(dir, ".observe.lock"), 50*time.Millisecond)
	if ok, err := lock.Acquire(0); err != nil || !ok {
		return false
	}
	defer lock.Release()
	path := filepath.Join(dir, "observe_state.json")
	var state struct {
		LastAt float64 `json:"lastAt"`
	}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	at := float64(now.UnixNano()) / 1e9
	if at-state.LastAt < observeGap.Seconds() && at >= state.LastAt {
		return false
	}
	state.LastAt = at
	raw, _ := json.Marshal(state)
	return atomicfile.Write(path, raw, atomicfile.Opts{}) == nil
}

// markObservedDue makes the observed profile's account due for a usage fetch,
// so the tick that follows reads it fresh (DESIGN A75).
func markObservedDue(sw *core.Switcher, group groups.ID) {
	scope := sw
	if group != "" {
		g, err := sw.ForGroup(group)
		if err != nil {
			return
		}
		scope = g
	}
	if num := scope.CurrentAccountNumber(); num != nil {
		reporting.MarkUsageDue(sw.Store, *num)
	}
}

// observedGroup is the session group whose profile the status line runs in,
// from the CLAUDE_CONFIG_DIR Claude Code passes it; "" for the default login.
func observedGroup(root string) groups.ID {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		return ""
	}
	for _, id := range groups.All() {
		if filepath.Clean(dir) == filepath.Clean(groups.ProfileDir(root, id)) {
			return id
		}
	}
	return ""
}
