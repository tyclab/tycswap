package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
)

type GroupLaunch struct {
	Group            groups.ID `json:"group"`
	AccountNumber    string    `json:"accountNumber"`
	Model            string    `json:"model"`
	ProfileDir       string    `json:"profileDir"`
	CWD              string    `json:"cwd"`
	Args             []string  `json:"args"`
	Env              []string  `json:"-"`
	LaunchID         string    `json:"launchId"`
	SessionID        string    `json:"sessionId,omitempty"`
	SourceSessionID  string    `json:"sourceSessionId,omitempty"`
	SourceTranscript string    `json:"sourceTranscript,omitempty"`
	Migrating        bool      `json:"migrating"`
	Warnings         []string  `json:"warnings,omitempty"`
}

type groupStoreProvider interface{ GroupStore() *store.Store }

func (m *Manager) PrepareGroup(group, account, model, resume, cwd string) (*GroupLaunch, error) {
	id, err := groups.Parse(group)
	if err != nil {
		return nil, err
	}
	if model == "" {
		model = string(id)
	}
	if err := groups.CheckModel(id, model); err != nil {
		return nil, err
	}
	provider, ok := m.accounts.(groupStoreProvider)
	if !ok {
		return nil, cerr.Session("this account store does not support session groups")
	}
	s, err := provider.GroupStore().ForGroup(id)
	if err != nil {
		return nil, err
	}
	intent := groups.Start
	if resume != "" {
		intent = groups.Continue
	}
	s = s.WithGroupIntent(intent).WithGroupHandoff()
	if err := switching.ReconcileGroup(s); err != nil {
		return nil, err
	}
	launch := &GroupLaunch{Group: id, Model: model, ProfileDir: s.ProfileDir(), CWD: cwd}
	if err := m.prepareGroupResume(s, launch, resume); err != nil {
		return nil, err
	}
	if launch.CWD == "" {
		launch.CWD, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	launch.CWD, err = filepath.Abs(launch.CWD)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(launch.CWD); err != nil || !info.IsDir() {
		return nil, cerr.Session("the session working directory is unavailable: %s", launch.CWD)
	}
	if account == "" {
		if current := s.CurrentAccountNumber(); current != nil {
			account = *current
		} else {
			candidates := s.SwitchableAccountNumbers()
			if len(candidates) == 0 {
				return nil, cerr.Session("no compatible, unclaimed account can %s a session in the %s group; inspect tycswap groups and account capabilities", intent, id)
			}
			account = candidates[0]
		}
	}
	number, _, _, err := s.ResolveAccount(account)
	if err != nil {
		return nil, err
	}
	if _, err := switching.SwitchTo(s, number, true, false); err != nil {
		return nil, err
	}
	launch.AccountNumber = number
	if err := m.prepareGroupSettings(s, model); err != nil {
		return nil, err
	}
	m.syncMCPServers(s.ProfileDir(), true)
	for _, name := range []string{"keybindings.json", "CLAUDE.md", "skills", "commands", "agents"} {
		source := filepath.Join(s.DefaultProfileDir(), name)
		target := filepath.Join(s.ProfileDir(), name)
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if err := createShare(source, target, m.accounts.Platform() != platform.Windows); err != nil {
			m.logWarnf("Could not share %s into %s group: %v", name, id, err)
		}
	}
	launch.LaunchID, err = newSessionUUID()
	if err != nil {
		return nil, err
	}
	launch.Args = append([]string{"--model", model}, launch.Args...)
	if resume == "" {
		launch.SessionID, err = newSessionUUID()
		if err != nil {
			return nil, err
		}
		launch.Args = append(launch.Args, "--session-id", launch.SessionID)
	}
	launch.Env = setEnvVar(scrubEnv(m.environ(), append(append([]string{}, AuthOverrideEnvVars...), "ANTHROPIC_BASE_URL", "CLAUDE_CONFIG_DIR")), "CLAUDE_CONFIG_DIR", launch.ProfileDir)
	launch.Env = setEnvVar(launch.Env, "TYCSWAP_GROUP_LAUNCH_ID", launch.LaunchID)
	launch.Env = setEnvVar(launch.Env, "TYCSWAP_GROUP", string(id))
	launch.Env = setEnvVar(launch.Env, "TYCSWAP_GROUP_MODEL", model)
	launch.Env = setEnvVar(launch.Env, "TYCSWAP_DEFAULT_PROFILE_DIR", s.DefaultProfileDir())
	launch.Env = setEnvVar(launch.Env, "TYCSWAP_DEFAULT_PROFILE_UNPINNED", strconv.FormatBool(s.DefaultProfileUnpinned()))
	launch.Env = setEnvVar(launch.Env, "TYCSWAP_DEFAULT_KEYCHAIN_SERVICE", s.DefaultKeychainService())
	if err := saveGroupLaunch(s.SharedRoot(), launch); err != nil {
		return nil, err
	}
	return launch, nil
}

func (m *Manager) RunGroup(group, account, model, resume, cwd string, args []string) error {
	model, resume, tail, err := normalizeGroupArgs(model, resume, args)
	if err != nil {
		return err
	}
	bin, err := m.runner.LookPath("claude")
	if err != nil || bin == "" {
		return errClaudeNotFound()
	}
	if err := CheckCmdShimArgs(bin, tail); err != nil {
		return err
	}
	launch, err := m.PrepareGroup(group, account, model, resume, cwd)
	if err != nil {
		return err
	}
	launch.Args = append(launch.Args, tail...)
	m.println(fmt.Sprintf("Launching %s group with Account-%s (%s).", launch.Group.Label(), launch.AccountNumber, launch.Model))
	if launch.Migrating {
		m.println("Forking the selected conversation into this group; its original history is preserved.")
	}
	if err := CheckCmdShimArgs(bin, launch.Args); err != nil {
		return err
	}
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

func normalizeGroupArgs(model, resume string, args []string) (string, string, []string, error) {
	tail := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, value, hasValue := strings.Cut(arg, "=")
		switch key {
		case "--model", "--resume", "-r":
			if !hasValue {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return "", "", nil, cerr.Session("%s requires an explicit value in a managed group", key)
				}
				i++
				value = args[i]
			}
			if key == "--model" {
				if model != "" && model != value {
					return "", "", nil, cerr.Session("conflicting model arguments")
				}
				model = value
			} else {
				if resume != "" && resume != value {
					return "", "", nil, cerr.Session("conflicting resume arguments")
				}
				resume = value
			}
		case "--continue", "-c", "--fork-session", "--session-id", "--settings", "--setting-sources", "--fallback-model", "--agents", "--agent":
			return "", "", nil, cerr.Session("%s bypasses managed session identity, settings or model guards; select a group, model and explicit resume ID through tycswap", key)
		default:
			tail = append(tail, arg)
		}
	}
	return model, resume, tail, nil
}

func newSessionUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	h := hex.EncodeToString(value[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
