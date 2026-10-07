package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/web"
)

type recoveryWeb struct {
	runtime       *recoveryRuntime
	lookPath      func(string) (string, error)
	command       func(context.Context, string, ...string) *exec.Cmd
	token         func(int) (string, string, error)
	reconcileMu   sync.Mutex
	reconciling   map[string]bool
	nextReconcile map[string]time.Time
}

func newRecoveryWeb(runtime *recoveryRuntime) web.RecoveryFacade {
	return &recoveryWeb{runtime: runtime, lookPath: exec.LookPath, command: exec.CommandContext}
}

func (f *recoveryWeb) View(snapshot reporting.AccountsSnapshot) web.RecoveryView {
	incidents, err := f.runtime.EvaluateSnapshot(snapshot)
	view := web.RecoveryView{Incidents: incidents, Coverage: "Managed Claude hooks and observed Codex app-server streams. Existing unmanaged sessions require restart; ordinary Codex TUI rendered rollout errors alone cannot trigger recovery.", WaitMinutes: settings.Load(f.runtime.sw.Store.SharedRoot()).HandoverWaitMinutes}
	view.SessionModels = map[string]string{}
	state, stateErr := f.runtime.store.Snapshot()
	if stateErr == nil {
		view.Handovers = state.Handovers
		for _, handover := range state.Handovers {
			if handover.Status == "prepared" {
				f.reconcileLater(handover)
			}
		}
		for id, session := range state.Sessions {
			if recovery.ModelClass(session.Event.Model) != "" {
				view.SessionModels[id] = session.Event.Model
			}
		}
	}
	if err != nil {
		view.Error = err.Error()
	}
	return view
}

func (f *recoveryWeb) Prepare(id string, source recovery.Source) (recovery.Packet, error) {
	return f.runtime.PrepareSession(id, source)
}
func (f *recoveryWeb) Dismiss(id, incident string) error {
	return f.runtime.store.Dismiss(id, incident)
}

func (f *recoveryWeb) Start(request web.RecoveryStartRequest) (web.RecoveryStartResult, error) {
	plan, err := f.runtime.PlanSession(request.Packet, request.Destination, request.ReviewedDigest, request.Explicit, request.PendingConfirmed)
	if err != nil {
		return web.RecoveryStartResult{}, err
	}
	result := web.RecoveryStartResult{Plan: plan}
	path, err := f.lookPath("flakelab")
	if err != nil {
		return f.manualPlan(plan, "Flakelab terminal launcher is unavailable; run the reviewed managed command in a terminal."), nil
	}
	if plan.Destination.Provider == "claude" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		data, err := f.command(ctx, path, "sessions", "--help").Output()
		cancel()
		if err != nil || !strings.Contains(string(data), "--group") {
			return f.manualPlan(plan, "Installed Flakelab lacks session group support; run the reviewed managed command in a terminal."), nil
		}
	}
	args := []string{"sessions", "--start", plan.Destination.Provider}
	if plan.Destination.Provider == "claude" {
		args = append(args, "--group", plan.Destination.Group)
	}
	args = append(args, plan.CWD, "--detach", "--")
	args = append(args, plan.Args...)
	if err := f.runtime.store.BeginHandover(plan, request.Packet.Source.IncidentID); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := f.command(ctx, path, args...)
	cmd.Env = handoverEnvironment(os.Environ(), plan)
	if err := cmd.Start(); err != nil {
		_ = f.runtime.store.CancelHandover(plan.CWD, plan.PacketDigest)
		return result, fmt.Errorf("destination could not start: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return result, fmt.Errorf("destination launch outcome is uncertain; inspect the terminal before retrying: %w", err)
	}
	result.Started = true
	if f.confirmDestination(path, plan) {
		result.Message = "Destination terminal started with its native session ID recorded. Editing ownership transferred; original source session is preserved."
		return result, nil
	}
	result.Message = "Destination terminal started. Editing ownership is reserved for it; source session is preserved. Native destination session ID confirmation is still required."
	return result, nil
}

func (f *recoveryWeb) manualPlan(plan recovery.LaunchPlan, message string) web.RecoveryStartResult {
	if plan.Destination.Provider == "claude" {
		plan.Executable = "tycswap"
		plan.Args = append([]string{"run", "--group", plan.Destination.Group, "--model", plan.Destination.Model, "--"}, plan.Args[len(plan.Args)-1])
	}
	return web.RecoveryStartResult{Plan: plan, Message: message}
}

func handoverEnvironment(env []string, plan recovery.LaunchPlan) []string {
	blocked := map[string]bool{"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR": true, "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR": true, "CLAUDE_CONFIG_DIR": true, "CLAUDE_SECURESTORAGE_CONFIG_DIR": true, "OPENAI_API_KEY": true, "CODEX_API_KEY": true, "CODEX_HOME": true, "ACCESS_TOKEN": true, "TYCSWAP_GROUP_LAUNCH_ID": true}
	out := []string{}
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok && !blocked[key] && !strings.HasPrefix(key, "TYCSWAP_HANDOVER_") {
			out = append(out, item)
		}
	}
	return append(out, "TYCSWAP_HANDOVER_CWD="+plan.CWD, "TYCSWAP_HANDOVER_DIGEST="+plan.PacketDigest)
}

var _ web.RecoveryFacade = (*recoveryWeb)(nil)

type launchedSession struct {
	Tool    string `json:"tool"`
	PID     int    `json:"pid"`
	Session string `json:"session"`
	CWD     string `json:"cwd"`
}

func (f *recoveryWeb) confirmDestination(path string, plan recovery.LaunchPlan) bool {
	state, err := f.runtime.store.Snapshot()
	if err == nil {
		if h, ok := state.Handovers[leaseWorkspace(plan.CWD)]; ok && h.Status == "active" && h.PacketDigest == plan.PacketDigest && h.DestinationSessionID != "" {
			return true
		}
	}
	for attempt := 0; attempt < 4; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		data, err := f.command(ctx, path, "sessions", "--json").Output()
		cancel()
		if err != nil {
			return false
		}
		var sessions []launchedSession
		if json.Unmarshal(data, &sessions) != nil {
			return false
		}
		matched := []launchedSession{}
		for _, session := range sessions {
			if session.Tool != plan.Destination.Provider || session.Session == "" || session.Session == plan.SourceSessionID || session.CWD != plan.CWD {
				continue
			}
			token := f.token
			if token == nil {
				token = processHandoverToken
			}
			cwd, digest, err := token(session.PID)
			if err == nil && cwd == plan.CWD && digest == plan.PacketDigest {
				matched = append(matched, session)
			}
		}
		if len(matched) == 1 {
			event := recovery.Event{Provider: plan.Destination.Provider, SessionID: matched[0].Session, CWD: plan.CWD, Group: plan.Destination.Group, Model: plan.Destination.Model, Kind: "SessionStart", At: time.Now(), TranscriptPath: processTranscript(matched[0].PID, matched[0].Session)}
			if event.Provider == "codex" {
				event.Group = "codex"
				event.Kind = "thread/started"
			}
			if _, err := f.runtime.store.Record(event); err != nil {
				return false
			}
			return f.runtime.store.ConfirmDestination(event, plan.CWD, plan.PacketDigest) == nil
		}
		if len(matched) > 1 {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func processHandoverToken(pid int) (string, string, error) {
	if runtime.GOOS != "linux" || pid <= 1 {
		return "", "", fmt.Errorf("process launch token unavailable")
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return "", "", err
	}
	cwd, digest := "", ""
	for _, item := range strings.Split(string(data), "\x00") {
		key, value, _ := strings.Cut(item, "=")
		switch key {
		case "TYCSWAP_HANDOVER_CWD":
			cwd = value
		case "TYCSWAP_HANDOVER_DIGEST":
			digest = value
		}
	}
	return cwd, digest, nil
}

func (f *recoveryWeb) reconcileLater(h recovery.Handover) {
	key := h.CWD + "\x00" + h.PacketDigest
	f.reconcileMu.Lock()
	if f.reconciling == nil {
		f.reconciling = map[string]bool{}
		f.nextReconcile = map[string]time.Time{}
	}
	if f.reconciling[key] || time.Now().Before(f.nextReconcile[key]) {
		f.reconcileMu.Unlock()
		return
	}
	f.reconciling[key] = true
	f.nextReconcile[key] = time.Now().Add(5 * time.Second)
	f.reconcileMu.Unlock()
	go func() {
		defer func() { f.reconcileMu.Lock(); delete(f.reconciling, key); f.reconcileMu.Unlock() }()
		path, err := f.lookPath("flakelab")
		if err != nil {
			return
		}
		f.confirmDestination(path, recovery.LaunchPlan{SourceSessionID: h.SourceSessionID, CWD: h.CWD, Destination: h.Destination, PacketDigest: h.PacketDigest})
	}()
}

func processTranscript(pid int, session string) string {
	if runtime.GOOS != "linux" || pid <= 1 || session == "" {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
	if err != nil {
		return ""
	}
	found := ""
	for _, entry := range entries {
		path, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
		if err != nil || !strings.HasSuffix(path, ".jsonl") || !strings.Contains(filepath.Base(path), session) {
			continue
		}
		if found != "" && found != path {
			return ""
		}
		found = path
	}
	return found
}

func leaseWorkspace(cwd string) string {
	workspace, err := recovery.WorkspaceKey(cwd)
	if err != nil {
		return cwd
	}
	return workspace
}
