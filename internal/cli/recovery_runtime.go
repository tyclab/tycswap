package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

type recoveryRuntime struct {
	mu       sync.Mutex
	snapshot reporting.AccountsSnapshot
	sw       *core.Switcher
	store    *recovery.Store
	rotate   func(group, account string) error
	wait     time.Duration
}

func newRecoveryRuntime(sw *core.Switcher, rotate func(group, account string) error) *recoveryRuntime {
	return &recoveryRuntime{sw: sw, store: recovery.NewStore(sw.Store.SharedRoot()), rotate: rotate, wait: recovery.DefaultWait}
}

func recoveryAccount(row reporting.AccountSnapshot, model string) recovery.Account {
	a := recovery.Account{ID: row.Number, Identity: recovery.IdentityKey(row.Email, row.OrgUUID), Eligible: row.RotationEligible, OwnershipKnown: true, Available: true}
	if row.Usage.FetchedAt != nil {
		a.FetchedAt = time.Unix(0, int64(*row.Usage.FetchedAt*1e9))
	}
	if row.Usage.Sentinel != "" || row.Usage.LastError != "" {
		return a
	}
	projected := oauth.NewUsage(row.Usage.LastGood)
	if projected == nil {
		return a
	}
	for _, w := range oauth.RelevantWindows(projected, nil) {
		reset, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
		a.Windows = append(a.Windows, recovery.Window{Name: w.Label, Used: w.Pct, ResetAt: reset})
	}
	scopedFound := false
	for _, w := range projected.Scoped {
		name := strings.ToLower(w.Name)
		class := ""
		switch {
		case strings.Contains(name, "fable"):
			class = "fable"
		case strings.Contains(name, "opus"):
			class = "opus"
		case strings.Contains(name, "sonnet"):
			class = "sonnet"
		case strings.Contains(name, "haiku"):
			class = "haiku"
		default:
			continue
		}
		reset, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
		a.Windows = append(a.Windows, recovery.Window{Name: w.Name, Model: class, Used: w.Pct, ResetAt: reset})
		if class == model {
			scopedFound = true
		}
	}
	a.QuotaKnown = len(a.Windows) > 0 && (model != "fable" || scopedFound)
	if row.ProviderName() == "claude" && (projected.FiveHour == nil || projected.SevenDay == nil) {
		a.QuotaKnown = false
	}
	return a
}

func (r *recoveryRuntime) accounts(snapshot reporting.AccountsSnapshot, group string, intent groups.Intent) ([]recovery.Account, string, error) {
	out := []recovery.Account{}
	current := ""
	if group == "codex" {
		for _, row := range snapshot.Accounts {
			if row.ProviderName() != "codex" {
				continue
			}
			a := recoveryAccount(row, "codex")
			out = append(out, a)
			if row.IsActive {
				current = row.Number
			}
		}
		return out, current, nil
	}
	id, err := groups.Parse(group)
	if err != nil {
		return nil, "", err
	}
	scoped, err := r.sw.Store.ForGroup(id)
	if err != nil {
		return nil, "", err
	}
	active, err := groups.LoadActive(r.sw.Store.SharedRoot(), id)
	if err != nil {
		return nil, "", err
	}
	if active != nil {
		current = active.Number
	}
	for _, row := range snapshot.Accounts {
		if row.ProviderName() != "claude" {
			continue
		}
		a := recoveryAccount(row, group)
		compatibility := scoped.GroupCompatibility(row.Number, intent)
		if !compatibility.Known {
			a.OwnershipKnown = false
		} else if !compatibility.Allowed {
			a.Eligible = false
		}
		owner, err := scoped.CredentialOwner(row.Number)
		applyRecoveryOwner(&a, owner, err, group)
		out = append(out, a)
	}
	return out, current, nil
}

func usableAccount(accounts []recovery.Account, current, model string, now time.Time) bool {
	for _, a := range accounts {
		if a.ID != current || !a.Eligible || !a.OwnershipKnown || !a.Available || !a.QuotaKnown || a.FetchedAt.IsZero() || a.FetchedAt.After(now) || now.Sub(a.FetchedAt) > recovery.FreshQuota {
			continue
		}
		relevant := false
		blocked := false
		for _, w := range a.Windows {
			if w.Model == "" || w.Model == model {
				relevant = true
				if w.Used >= 100 {
					blocked = true
				}
			}
		}
		if relevant && !blocked {
			return true
		}
	}
	return false
}

func (r *recoveryRuntime) destinationUsable(snapshot reporting.AccountsSnapshot, provider string, now time.Time) bool {
	groupsToCheck := []string{"codex"}
	if provider == "codex" {
		groupsToCheck = []string{"opus", "fable"}
	}
	for _, group := range groupsToCheck {
		accounts, current, err := r.accounts(snapshot, group, groups.Start)
		if err == nil && usableAccount(accounts, current, group, now) {
			return true
		}
	}
	return false
}

func (r *recoveryRuntime) EvaluateSnapshot(snapshot reporting.AccountsSnapshot) ([]recovery.Incident, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot = snapshot
	r.wait = time.Duration(settings.Load(r.sw.Store.SharedRoot()).HandoverWaitMinutes) * time.Minute
	state, err := r.store.Snapshot()
	if err != nil {
		return nil, err
	}
	out := []recovery.Incident{}
	now := time.Now()
	ids := make([]string, 0, len(state.Sessions))
	for id := range state.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ss := state.Sessions[id]
		e := ss.Event
		if !ss.Idle || !e.Stopped {
			continue
		}
		accounts, current, err := r.accounts(snapshot, e.Group, groups.Continue)
		if err != nil {
			out = append(out, recovery.Incident{Event: e, Decision: recovery.Decision{Action: "unknown", Reason: err.Error()}})
			continue
		}
		in := recovery.Input{Event: e, CurrentAccount: current, Accounts: accounts, DestinationUsable: r.destinationUsable(snapshot, e.Provider, now), Now: now, Wait: r.wait}
		decision := recovery.Evaluate(in)
		if decision.Action == "rotate" && r.rotate != nil {
			if err := r.rotate(e.Group, decision.Account); err != nil {
				decision.Action = "unknown"
				decision.Reason = "Compatible account rotation could not complete: " + err.Error()
			} else {
				decision.Action = "wait"
				decision.Reason = "Compatible account rotated; continue in the source session"
				if e.Provider == "codex" {
					decision.Reason = "Compatible Codex account selected; restart and resume the source conversation to use it"
				}
			}
			decision, err = r.store.WriteDecision(e, decision)
			if err != nil {
				return nil, err
			}
			out = append(out, recovery.Incident{Event: e, Decision: decision})
			continue
		}
		decision, err = r.store.Decide(in)
		if err != nil {
			return nil, err
		}
		out = append(out, recovery.Incident{Event: e, Decision: decision, Offered: decision.Action == "offer", Dismissed: decision.Reason == "Handover dismissed for this incident"})
	}
	return out, nil
}

func (r *recoveryRuntime) PrepareSession(id string, supplied recovery.Source) (recovery.Packet, error) {
	state, err := r.store.Snapshot()
	if err != nil {
		return recovery.Packet{}, err
	}
	ss, ok := state.Sessions[id]
	if !ok || (!ss.Event.Stopped && ss.Event.Kind != "ManualSelection") {
		e, err := r.selectManualSource(id, ss.Event)
		if err != nil {
			return recovery.Packet{}, err
		}
		e, err = r.store.Record(e)
		if err != nil {
			return recovery.Packet{}, err
		}
		ss = recovery.SessionState{Event: e, Idle: true}
		supplied.Omissions = append(supplied.Omissions, "Manually selected idle session. Saved tool state and background commands still require review.")
	}
	supplied.Provider = ss.Event.Provider
	supplied.SessionID = id
	supplied.IncidentID = ss.Event.IncidentID
	supplied.Group = ss.Event.Group
	supplied.CWD = ss.Event.CWD
	supplied.Messages = nil
	if ss.Event.TranscriptPath != "" {
		messages, omissions, err := recovery.ReadTranscript(ss.Event.TranscriptPath, ss.Event.Provider)
		if err != nil {
			supplied.Omissions = append(supplied.Omissions, "Saved transcript unavailable: "+err.Error())
		} else {
			supplied.Messages = messages
			supplied.Omissions = append(supplied.Omissions, omissions...)
			if supplied.Checkpoint == "" {
				saved, err := recovery.ReadCheckpoint(ss.Event.TranscriptPath, ss.Event.Provider)
				if err == nil {
					supplied.Checkpoint = saved
				}
			}
		}
	} else {
		supplied.Omissions = append(supplied.Omissions, "Saved transcript path is unavailable")
	}
	supplied.Worktree = gitRead(supplied.CWD, "rev-parse", "--show-toplevel")
	supplied.Branch = gitRead(supplied.CWD, "symbolic-ref", "--short", "HEAD")
	supplied.ChangedFiles = gitChangedFiles(supplied.CWD)
	if supplied.Worktree == "" {
		supplied.Omissions = append(supplied.Omissions, "Git workspace state could not be established")
	}
	supplied.Omissions = append(supplied.Omissions, "Review pending and background commands before transferring editing ownership")
	return recovery.Prepare(supplied)
}

func gitRead(cwd string, args ...string) string {
	if cwd == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	data, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (r *recoveryRuntime) PlanSession(packet recovery.Packet, dest recovery.Destination, reviewDigest string, explicit, pendingConfirmed bool) (recovery.LaunchPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.store.Snapshot()
	if err != nil {
		return recovery.LaunchPlan{}, err
	}
	ss, ok := state.Sessions[packet.Source.SessionID]
	if !ok || ss.Event.IncidentID != packet.Source.IncidentID || ss.Event.Provider != packet.Source.Provider || ss.Event.CWD != packet.Source.CWD || !ss.Idle || (!ss.Event.Stopped && ss.Event.Kind != "ManualSelection") {
		return recovery.LaunchPlan{}, errors.New("source changed or is not stopped and idle")
	}
	if err := r.conflictingEditors(packet.Source.SessionID, packet.Source.CWD); err != nil {
		return recovery.LaunchPlan{}, err
	}
	sourceAlive := false
	if ss.Event.Provider == "codex" {
		if ss.Event.SourcePID <= 1 {
			return recovery.LaunchPlan{}, errors.New("Codex source process identity is unknown; an owning app-server host must record its PID")
		}
		if procdetect.IsPIDAlive(ss.Event.SourcePID) {
			return recovery.LaunchPlan{}, errors.New("stop the Codex source process before handover; a passive observer cannot prevent later turns")
		}
	}
	if ss.Event.Provider == "claude" {
		profile := ss.Event.ProfileDir
		if id, err := groups.Parse(ss.Event.Group); err == nil {
			profile = groups.ProfileDir(r.sw.Store.SharedRoot(), id)
		}
		if profile == "" {
			return recovery.LaunchPlan{}, errors.New("source profile is unknown")
		}
		sessions, err := strictProfileSessions(profile)
		if err != nil {
			return recovery.LaunchPlan{}, err
		}
		for _, live := range sessions {
			if live.SessionID != packet.Source.SessionID {
				continue
			}
			sourceAlive = true
			if ss.Event.Kind == "ManualSelection" && ss.Event.Group == "" {
				return recovery.LaunchPlan{}, errors.New("stop the unmanaged source process before handover; it has no editing ownership hook")
			}
			if live.Status != nil && *live.Status != "idle" {
				return recovery.LaunchPlan{}, fmt.Errorf("source session is %s", *live.Status)
			}
			if live.Kind == "bg" {
				return recovery.LaunchPlan{}, errors.New("background source session cannot transfer edit ownership")
			}
		}
	}
	if ss.Event.TranscriptPath != "" {
		tools, err := recovery.InspectTools(ss.Event.TranscriptPath, ss.Event.Provider)
		if err != nil {
			return recovery.LaunchPlan{}, err
		}
		if !tools.Known {
			return recovery.LaunchPlan{}, errors.New("saved tool state is incomplete or too large; wait for transcript writes to finish before handover")
		}
		if tools.Background && (sourceAlive || !pendingConfirmed) {
			return recovery.LaunchPlan{}, errors.New("background tool history requires a stopped source process and explicit verification that background commands ended")
		}
		if tools.Pending > 0 {
			return recovery.LaunchPlan{}, fmt.Errorf("source has %d unresolved saved tool calls", tools.Pending)
		}
	}
	group := dest.Group
	if dest.Provider == "codex" {
		group = "codex"
	}
	accounts, current, err := r.accounts(r.snapshot, group, groups.Start)
	quotaModel := recovery.ModelFamily(dest.Model)
	if quotaModel == "" {
		quotaModel = group
	}
	dest.Usable = err == nil && usableAccount(accounts, current, quotaModel, time.Now())
	if !pendingConfirmed {
		return recovery.LaunchPlan{}, errors.New("confirm no pending tools, commands, approvals or background edits after reviewing omissions")
	}
	return recovery.Plan(packet, dest, recovery.StartRequest{Explicit: explicit, ReviewedDigest: reviewDigest, SourceIdle: true, StateKnown: true, PendingTools: ss.PendingTools})
}

func gitChangedFiles(cwd string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "git", "-C", cwd, "status", "--porcelain=v1", "-z").Output()
	if err != nil {
		return nil
	}
	entries := strings.Split(string(data), "\x00")
	out := []string{}
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		out = append(out, entry[3:])
		if entry[0] == 'R' || entry[0] == 'C' || entry[1] == 'R' || entry[1] == 'C' {
			i++
			if i < len(entries) && entries[i] != "" {
				out = append(out, entries[i])
			}
		}
	}
	return out
}

func (r *recoveryRuntime) selectManualSource(id string, previous recovery.Event) (recovery.Event, error) {
	live := web.SessionsIn(r.sw.Store.SharedRoot())()
	var selected *procdetect.ClaudeSession
	for _, session := range live.Claude {
		if session.SessionID == id {
			if selected != nil {
				return recovery.Event{}, errors.New("source session identity is ambiguous")
			}
			copy := session
			selected = &copy
		}
	}
	if selected == nil {
		return recovery.Event{}, errors.New("source is not recorded and no matching live session was found; use recovery prepare with an explicitly selected saved transcript")
	}
	if selected.Status == nil || *selected.Status != "idle" || selected.Kind == "bg" {
		return recovery.Event{}, errors.New("manually selected source must report idle and have no background editor")
	}
	profile := live.ConfigDir[selected.PID]
	group := live.Profile[selected.PID]
	if group != "fable" && group != "opus" {
		group = ""
	}
	return recovery.Event{Provider: "claude", SessionID: id, Group: group, Model: previous.Model, Kind: "ManualSelection", At: time.Now(), CWD: selected.CWD, ProfileDir: profile, TranscriptPath: web.TranscriptPath(profile, selected.CWD, id)}, nil
}

func strictProfileSessions(profile string) ([]procdetect.ClaudeSession, error) {
	entries, err := os.ReadDir(filepath.Join(profile, "sessions"))
	if os.IsNotExist(err) {
		return procdetect.ListSessionsErr(profile)
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(profile, "sessions", entry.Name()))
		if err != nil {
			return nil, err
		}
		var record struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &record) != nil || record.PID <= 1 {
			return nil, errors.New("source process registry is malformed; cannot establish quiescence")
		}
		if record.SessionID == "" && procdetect.IsPIDAlive(record.PID) {
			return nil, errors.New("live source process registry identity is unknown")
		}
	}
	return procdetect.ListSessionsErr(profile)
}

func (r *recoveryRuntime) conflictingEditors(source, cwd string) error {
	workspace, err := recovery.WorkspaceKey(cwd)
	if err != nil {
		return err
	}
	roots := []string{paths.GetClaudeConfigHome()}
	for _, id := range groups.All() {
		roots = append(roots, groups.ProfileDir(r.sw.Store.SharedRoot(), id))
	}
	entries, err := os.ReadDir(filepath.Join(r.sw.Store.SharedRoot(), "sessions"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			roots = append(roots, filepath.Join(r.sw.Store.SharedRoot(), "sessions", entry.Name()))
		}
	}
	for _, profile := range roots {
		sessions, err := strictProfileSessions(profile)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if session.SessionID == source {
				continue
			}
			other, err := recovery.WorkspaceKey(session.CWD)
			if err != nil {
				return err
			}
			if other == workspace && (session.Status == nil || *session.Status != "idle" || session.Kind == "bg") {
				return errors.New("another session is working in this worktree; wait for it to stop before transferring editing ownership")
			}
		}
	}
	return nil
}

func applyRecoveryOwner(account *recovery.Account, owner groups.Owner, err error, group string) {
	account.OwnershipKnown = account.OwnershipKnown && err == nil && !owner.Uncertain
	account.Available = err == nil && (owner.Scope == "" || owner.Scope == group) && !owner.Uncertain
	if err == nil && !owner.Uncertain && owner.Scope != "" && owner.Scope != group {
		account.Eligible = false
	}
}
