package session

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/store"
)

//go:embed routingplugin/register.js
var routingPlugin []byte

//go:embed routingplugin/check.test.ts
var routingPluginCheck []byte

var claudeVersion = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func supportsRouting(version string) bool {
	parts := claudeVersion.FindStringSubmatch(version)
	if len(parts) != 4 {
		return false
	}
	major, _ := strconv.Atoi(parts[1])
	minor, _ := strconv.Atoi(parts[2])
	patch, _ := strconv.Atoi(parts[3])
	return major > 2 || major == 2 && (minor > 1 || minor == 1 && patch >= 309)
}

func routedFamily(model string) groups.ID {
	family, err := routingstate.Family(model)
	if err != nil {
		return ""
	}
	if family == "fable" {
		return groups.Fable
	}
	return groups.Opus
}

func (m *Manager) managedArguments(args []string) (account, model, resume string, tail []string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			tail = append(tail, args[i:]...)
			break
		}
		key, value, hasValue := strings.Cut(arg, "=")
		switch key {
		case "--model", "--account", "--resume", "-r":
			if !hasValue {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return "", "", "", nil, cerr.Session("%s requires an explicit value", key)
				}
				i++
				value = args[i]
			}
			var target *string
			switch key {
			case "--model":
				target = &model
			case "--account":
				target = &account
			default:
				target = &resume
			}
			if value == "" || *target != "" && *target != value {
				return "", "", "", nil, cerr.Session("empty or conflicting %s arguments", key)
			}
			*target = value
		case "--settings", "--setting-sources", "--plugin-dir", "--plugin-url", "--bare", "--safe-mode", "--continue", "-c", "--fork-session", "--session-id", "--fallback-model", "--teammate-mode", "--team-name", "--agent-id", "--agent-name", "--parent-session-id":
			return "", "", "", nil, cerr.Session("%s is unavailable with automatic routing; use --resume ID and --model MODEL", key)
		default:
			tail = append(tail, arg)
		}
	}
	return
}

func (m *Manager) defaultRoutingModel() (string, error) {
	if value := m.getenv("ANTHROPIC_MODEL"); value != "" {
		return value, nil
	}
	provider, ok := m.accounts.(groupStoreProvider)
	if !ok {
		return "", cerr.Session("this account store does not support automatic routing")
	}
	config, err := ccfile.ReadGlobalConfigStrict(filepath.Join(provider.GroupStore().DefaultProfileDir(), "settings.json"))
	if err != nil {
		return "", err
	}
	if model, ok := config["model"].(string); ok && model != "" {
		return model, nil
	}
	if value := m.getenv("ANTHROPIC_DEFAULT_MODEL"); value != "" {
		return value, nil
	}
	return "opus", nil
}

func materializeRoutingPlugin(root string) (string, error) {
	digest := sha256.Sum256(append(append([]byte{}, routingPlugin...), routingPluginCheck...))
	dir := filepath.Join(root, "plugins", "routing-"+hex.EncodeToString(digest[:8]))
	files := map[string][]byte{
		".claude-plugin/plugin.json": []byte(`{"name":"tycswap-routing","version":"1.0.0","description":"Automatic account selection for model requests","license":"MIT"}`),
		"hooks/hooks.json":           []byte(`{"modules":["./register.js"]}`),
		"hooks/register.js":          routingPlugin,
		"check.test.ts":              routingPluginCheck,
	}
	for name, contents := range files {
		path := filepath.Join(dir, name)
		if err := groups.SafePath(path); err != nil {
			return "", err
		}
		if existing, err := os.ReadFile(path); err == nil && string(existing) == string(contents) {
			continue
		}
		if err := atomicfile.Write(path, contents, atomicfile.Opts{}); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func (m *Manager) PrepareManaged(account, model, resume, cwd string) (*GroupLaunch, error) {
	return m.prepareManaged("", account, model, resume, cwd)
}

func (m *Manager) managedResume(group, resume string) (*routingstate.Session, error) {
	if resume == "" {
		return nil, nil
	}
	if group != "" {
		id, err := groups.Parse(group)
		if err != nil {
			return nil, err
		}
		group = string(id)
	}
	provider, ok := m.accounts.(groupStoreProvider)
	if !ok {
		return nil, nil
	}
	state, err := routingstate.Read(provider.GroupStore().SharedRoot())
	if err != nil {
		return nil, err
	}
	id := strings.TrimSuffix(filepath.Base(resume), ".jsonl")
	var found *routingstate.Session
	for _, saved := range state.History {
		if saved.ID != id || group != "" && saved.Group != group {
			continue
		}
		if !validSessionID(resume) {
			path, err := filepath.Abs(resume)
			if err != nil {
				return nil, err
			}
			if !withinProfile(groups.ProfileDir(provider.GroupStore().SharedRoot(), groups.ID(saved.Group)), path) {
				continue
			}
		}
		if found != nil {
			return nil, cerr.Session("saved session exists in several routing profiles; provide its absolute transcript path")
		}
		copy := saved
		found = &copy
	}
	if found == nil {
		var newest time.Time
		for _, profile := range groups.All() {
			if group != "" && string(profile) != group {
				continue
			}
			if !validSessionID(resume) {
				path, err := filepath.Abs(resume)
				if err != nil {
					return nil, err
				}
				if !withinProfile(groups.ProfileDir(provider.GroupStore().SharedRoot(), profile), path) {
					continue
				}
			}
			dir := filepath.Join(groups.ScopeDir(provider.GroupStore().SharedRoot(), profile), "launches")
			entries, err := os.ReadDir(dir)
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				var launch groupLaunchRecord
				if err := groups.ReadJSON(filepath.Join(dir, entry.Name()), &launch); err != nil {
					return nil, err
				}
				if !launch.Routing || launch.SessionID != id && launch.ActualSessionID != id {
					continue
				}
				if found != nil && found.Group != string(profile) {
					return nil, cerr.Session("saved session exists in several routing profiles; provide its absolute transcript path")
				}
				if found == nil || launch.CreatedAt.After(newest) {
					newest = launch.CreatedAt
					found = &routingstate.Session{Group: string(profile), ID: id, Model: launch.Model, Launch: launch.LaunchID}
				}
			}
		}
	}
	return found, nil
}

func (m *Manager) managedTarget(group, model, resume string) (groups.ID, string, *routingstate.Session, error) {
	saved, err := m.managedResume(group, resume)
	if err != nil {
		return "", "", nil, err
	}
	if saved != nil {
		group = saved.Group
		if model == "" {
			model = saved.Model
		}
	}
	if model == "" {
		model, err = m.defaultRoutingModel()
		if err != nil {
			return "", "", nil, err
		}
	}
	id := routedFamily(model)
	if id == "" {
		return "", "", nil, cerr.Session("model %q has unknown routing compatibility; select an explicit Fable, Opus, Sonnet or Haiku model", model)
	}
	if group != "" {
		id, err = groups.Parse(group)
		if err != nil {
			return "", "", nil, err
		}
	}
	return id, model, saved, nil
}

func (m *Manager) prepareManaged(group, account, model, resume, cwd string) (*GroupLaunch, error) {
	provider, ok := m.accounts.(groupStoreProvider)
	if !ok {
		return nil, cerr.Session("this account store does not support automatic routing")
	}
	id, model, saved, err := m.managedTarget(group, model, resume)
	if err != nil {
		return nil, err
	}
	root := provider.GroupStore().SharedRoot()
	for _, profile := range []string{provider.GroupStore().DefaultProfileDir(), groups.ProfileDir(root, id)} {
		config, err := ccfile.ReadGlobalConfigStrict(filepath.Join(profile, "settings.json"))
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"disableAllHooks", "allowManagedHooksOnly", "allowManagedModsOnly", "disableCommandPluginSources"} {
			if disabled, _ := config[key].(bool); disabled {
				return nil, cerr.Session("automatic routing is unavailable while %s is enabled", key)
			}
		}
		if fallback, exists := config["fallbackModel"]; exists && fallback != nil && fallback != "" {
			return nil, cerr.Session("automatic routing does not support a settings fallbackModel")
		}
		if env, ok := config["env"].(map[string]any); ok {
			for key := range env {
				if strings.HasPrefix(key, "TYCSWAP_ROUTE_") || key == "TYCSWAP_ROUTING" || key == "TYCSWAP_GROUP" || strings.HasPrefix(key, "TYCSWAP_GROUP_") || strings.HasPrefix(key, "TYCSWAP_DEFAULT_") {
					return nil, cerr.Session("settings env must not override automatic routing variable %s", key)
				}
			}
		}
	}
	plugin, err := materializeRoutingPlugin(root)
	if err != nil {
		return nil, err
	}
	var launch *GroupLaunch
	err = routingstate.With(root, func(state *routingstate.State) error {
		for _, active := range state.Sessions {
			if active.Group == string(id) && active.Launch == "" {
				return cerr.Session("profile %s has a live legacy launch; exit it before starting automatic routing", id)
			}
		}
		pids, err := store.LiveProfilePIDs(groups.ProfileDir(root, id))
		if err != nil {
			return err
		}
		native, ides, err := procdetect.GetRunningInstancesErr(groups.ProfileDir(root, id))
		if err != nil {
			return err
		}
		allowed := map[int]bool{}
		seen := map[string]int{}
		for _, process := range native {
			existing, ok := state.Sessions[routingstate.Key(string(id), process.SessionID)]
			if !ok || existing.Launch == "" || runtime.GOOS != "windows" && existing.PID != process.PID {
				return cerr.Session("profile %s has an unmanaged live session (PID %d); exit it before starting automatic routing", id, process.PID)
			}
			if previous, exists := seen[process.SessionID]; exists && previous != process.PID {
				return cerr.Session("profile %s has several processes for the same conversation", id)
			}
			seen[process.SessionID] = process.PID
			allowed[process.PID] = true
		}
		for _, pid := range pids {
			if !allowed[pid] {
				return cerr.Session("profile %s has an unmanaged live process (PID %d)", id, pid)
			}
		}
		if len(ides) != 0 {
			return cerr.Session("profile %s has a live IDE instance; exit it before starting automatic routing", id)
		}
		if saved != nil {
			if _, exists := state.Sessions[routingstate.Key(saved.Group, saved.ID)]; exists {
				return cerr.Session("the selected conversation is already running")
			}
		}
		launch, err = m.prepareGroup(string(id), account, model, resume, cwd, true)
		if err != nil {
			return err
		}
		binary := m.hookExecutable
		if binary == "" {
			binary, err = os.Executable()
		}
		if err != nil {
			return err
		}
		binary, err = filepath.Abs(binary)
		if err != nil {
			return err
		}
		launch.Env = setEnvVar(launch.Env, "TYCSWAP_ROUTING", "1")
		launch.Env = setEnvVar(launch.Env, "TYCSWAP_ROUTE_BINARY", binary)
		launch.Env = setEnvVar(launch.Env, "TYCSWAP_ROUTE_LAUNCH", launch.LaunchID)
		launch.Env = setEnvVar(launch.Env, "TYCSWAP_ROUTE_PID", strconv.Itoa(os.Getpid()))
		launch.Args = append(launch.Args, "--plugin-dir", plugin)
		reservation := launch.SessionID
		if reservation == "" {
			reservation = launch.LaunchID
		}
		state.Sessions[routingstate.Key(string(id), reservation)] = routingstate.Session{Group: string(id), ID: reservation, PID: os.Getpid(), Model: model, Launch: launch.LaunchID}
		leaseID := "startup-" + launch.LaunchID
		state.Leases[routingstate.Key(string(id), leaseID)] = routingstate.Lease{ID: leaseID, Group: string(id), Session: reservation, Account: launch.AccountNumber, Model: model, PID: os.Getpid()}
		return nil
	})
	return launch, err
}

func (m *Manager) RunManaged(account, model, resume, cwd string, args []string) error {
	return m.runManaged("", account, model, resume, cwd, args)
}

func (m *Manager) runManaged(group, account, model, resume, cwd string, args []string) error {
	parsedAccount, parsedModel, parsedResume, tail, err := m.managedArguments(args)
	if err != nil {
		return err
	}
	for _, pair := range [][2]*string{{&account, &parsedAccount}, {&model, &parsedModel}, {&resume, &parsedResume}} {
		if *pair[1] != "" {
			if *pair[0] != "" && *pair[0] != *pair[1] {
				return cerr.Session("conflicting automatic routing arguments")
			}
			*pair[0] = *pair[1]
		}
	}
	bin, err := m.runner.LookPath("claude")
	if err != nil || bin == "" {
		return errClaudeNotFound()
	}
	version, rc, err := m.runner.Probe([]string{bin, "--version"}, m.environ(), 10*time.Second)
	if err != nil || rc != 0 || !supportsRouting(version) {
		return cerr.Session("automatic routing requires Claude Code 2.1.309 or later")
	}
	if err := CheckCmdShimArgs(bin, tail); err != nil {
		return err
	}
	provider, ok := m.accounts.(groupStoreProvider)
	if !ok {
		return cerr.Session("this account store does not support automatic routing")
	}
	root := provider.GroupStore().SharedRoot()
	plugin, err := materializeRoutingPlugin(root)
	if err != nil {
		return err
	}
	id, model, _, err := m.managedTarget(group, model, resume)
	if err != nil {
		return err
	}
	scope, err := provider.GroupStore().ForGroup(id)
	if err != nil {
		return err
	}
	preview := &GroupLaunch{Group: id, ProfileDir: scope.ProfileDir(), CWD: cwd}
	if err := m.prepareGroupResume(scope, preview, resume); err != nil {
		return err
	}
	probeEnv := setEnvVar(m.environ(), "CLAUDE_CONFIG_DIR", scope.ProfileDir())
	probeArgs := []string{bin, "plugin", "test", plugin}
	if runner, ok := m.runner.(interface {
		ProbeInDir([]string, []string, time.Duration, string) (string, int, error)
	}); ok {
		_, rc, err = runner.ProbeInDir(probeArgs, probeEnv, 20*time.Second, preview.CWD)
	} else {
		_, rc, err = m.runner.Probe(probeArgs, probeEnv, 20*time.Second)
	}
	if err != nil || rc != 0 {
		return cerr.Session("the account-routing mod cannot run in this Claude configuration")
	}
	launch, err := m.prepareManaged(group, account, model, resume, cwd)
	if err != nil {
		return err
	}
	defer func() {
		_ = routingstate.With(root, func(state *routingstate.State) error {
			for key, session := range state.Sessions {
				if session.Launch == launch.LaunchID {
					delete(state.Sessions, key)
				}
			}
			for key, lease := range state.Leases {
				if _, exists := state.Sessions[routingstate.Key(lease.Group, lease.Session)]; !exists {
					delete(state.Leases, key)
				}
			}
			return nil
		})
	}()
	launch.Args = append(launch.Args, tail...)
	if err := CheckCmdShimArgs(bin, launch.Args); err != nil {
		return err
	}
	m.println(fmt.Sprintf("Launching %s with automatic account routing.", launch.Model))
	current, _ := os.Getwd()
	if filepath.Clean(current) != filepath.Clean(launch.CWD) {
		if runner, ok := m.runner.(interface {
			ExecInDir(string, []string, []string, string) error
		}); ok {
			return runner.ExecInDir(bin, append([]string{bin}, launch.Args...), launch.Env, launch.CWD)
		}
		return cerr.Session("resume this session from its saved working directory: %s", launch.CWD)
	}
	return m.exec(bin, launch.Args, launch.Env)
}
