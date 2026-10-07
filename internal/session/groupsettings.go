package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
)

func (m *Manager) prepareGroupSettings(s *store.Store, model string) error {
	return s.Lock.With(func() error {
		path := s.SettingsPath()
		if err := groups.SafePath(path); err != nil {
			return err
		}
		config, err := ccfile.ReadGlobalConfigStrict(path)
		if err != nil {
			return err
		}
		if config == nil {
			config, err = ccfile.ReadGlobalConfigStrict(filepath.Join(s.DefaultProfileDir(), "settings.json"))
			if err != nil {
				return fmt.Errorf("cannot copy the default settings into the new group: %w", err)
			}
		}
		if config == nil {
			config = map[string]any{}
		}
		delete(config, "apiKeyHelper")
		if env, ok := config["env"].(map[string]any); ok {
			for _, key := range append(append([]string{}, AuthOverrideEnvVars...), "ANTHROPIC_BASE_URL", "CLAUDE_CONFIG_DIR") {
				delete(env, key)
			}
		}
		binary := m.hookExecutable
		if binary == "" {
			binary, err = os.Executable()
			if err != nil {
				return err
			}
		}
		binary, err = filepath.Abs(binary)
		if err != nil {
			return err
		}
		quoted := "'" + strings.ReplaceAll(binary, "'", "'\\''") + "'"
		modelVariable := `"$TYCSWAP_GROUP_MODEL"`
		if s.Platform == platform.Windows {
			quoted = strconv.Quote(binary)
			modelVariable = `"%TYCSWAP_GROUP_MODEL%"`
		}
		record := quoted + " recovery record --group " + string(s.GroupID()) + " --model " + modelVariable
		guard := quoted + " groups guard --group " + string(s.GroupID())
		installed := map[string]string{}
		sidecar := filepath.Join(s.ScopeRoot(), "hooks.json")
		if err := groups.ReadJSON(sidecar, &installed); err != nil {
			return err
		}
		hooks, ok := config["hooks"].(map[string]any)
		if !ok && config["hooks"] != nil {
			return fmt.Errorf("the group's settings hooks must be an object")
		}
		if hooks == nil {
			hooks = map[string]any{}
		}
		commands := map[string]string{"SessionStart": record, "UserPromptSubmit": record, "Stop": record, "StopFailure": record, "SessionEnd": record, "PreModelSwitch": guard}
		for event, command := range commands {
			entries, ok := hooks[event].([]any)
			if !ok && hooks[event] != nil {
				return fmt.Errorf("the group's %s hooks must be an array", event)
			}
			entries = removeInstalledHook(entries, installed[event], command)
			entries = append(entries, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 10}}})
			hooks[event] = entries
		}
		config["hooks"] = hooks
		config["disableAllHooks"] = false
		config["model"] = model
		if err := groups.WriteJSON(path, config); err != nil {
			return err
		}
		return groups.WriteJSON(sidecar, commands)
	})
}

func removeInstalledHook(entries []any, previous, current string) []any {
	out := make([]any, 0, len(entries))
	for _, value := range entries {
		entry, ok := value.(map[string]any)
		if !ok {
			out = append(out, value)
			continue
		}
		actions, ok := entry["hooks"].([]any)
		if !ok {
			out = append(out, value)
			continue
		}
		kept := make([]any, 0, len(actions))
		for _, action := range actions {
			command, ok := action.(map[string]any)
			if ok && (command["command"] == current || previous != "" && command["command"] == previous) {
				continue
			}
			kept = append(kept, action)
		}
		if len(kept) > 0 {
			entry["hooks"] = kept
			out = append(out, entry)
		}
	}
	return out
}
