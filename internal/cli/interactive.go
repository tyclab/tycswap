package cli

import (
	"strings"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
)

func promptMissingArguments(argv []string, s ioStreams) (int, bool) {
	if len(argv) == 0 {
		return 0, false
	}
	provider, start := reporting.ProviderClaude, 0
	if argv[0] == "codex" {
		provider, start = reporting.ProviderCodex, 1
		for start < len(argv) && argv[start] == "--debug" {
			start++
		}
		if start == len(argv) {
			return 0, false
		}
	}
	verb := argv[start]
	if verb == "config" && start == 0 {
		return promptConfigArguments(argv, s)
	}
	var required int
	switch verb {
	case "remove", "disable", "enable":
		required = 1
	case "rm":
		if provider != reporting.ProviderClaude {
			return 0, false
		}
		required = 1
	case "alias", "swap", "move":
		required = 2
	default:
		return 0, false
	}
	flags, pos, ok := interactivePositionals(argv[start+1:], func(flag string) bool {
		return flag == "--debug" || verb == "alias" && flag == "--unset" ||
			provider == reporting.ProviderCodex && verb == "remove" && (flag == "--yes" || flag == "-y")
	})
	if !ok {
		return 0, false
	}
	unset := contains(flags, "--unset")
	if verb == "alias" {
		if provider == reporting.ProviderClaude && len(pos) == 0 && !unset ||
			provider == reporting.ProviderCodex && len(pos) > 0 {
			return 0, false
		}
		if unset {
			required = 1
		}
	}
	if len(pos) >= required {
		return 0, false
	}
	if code, blocked := guardRoot(s.err); blocked {
		return code, true
	}
	var selected []reporting.AccountSnapshot
	for len(pos) < required {
		if len(pos) == 0 || verb == "swap" {
			title := "Choose the account"
			if len(pos) == 1 {
				title = "Swap with which account?"
			}
			a, code := selectProviderAccount(s, title, provider)
			if code != 0 {
				return code, true
			}
			selected = append(selected, a)
			pos = append(pos, a.Number)
			continue
		}
		title := "Destination slot (an occupied slot is swapped)"
		initial := ""
		if verb == "alias" {
			title = "Account name (empty clears it)"
			if len(selected) > 0 {
				initial = selected[0].Alias
			}
		}
		_, value, ok := askMenu(s, title, nil, initial)
		if !ok {
			return 130, true
		}
		if verb == "alias" && strings.TrimSpace(value) == "" {
			flags = append(flags, "--unset")
			break
		}
		pos = append(pos, value)
	}
	for _, account := range selected {
		if !menuAccountCurrent(s, account) {
			return 1, true
		}
	}
	args := append([]string(nil), argv[:start+1]...)
	args = append(args, flags...)
	args = append(args, pos...)
	return menuExecute(s, args...), true
}

// Only incomplete, recognized grammars enter a prompt. Everything else goes to
// the existing parser, including help, JSON, unknown flags and complete commands.
func interactivePositionals(args []string, allowed func(string) bool) (flags, pos []string, ok bool) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && arg != "-" {
			if !allowed(arg) {
				return nil, nil, false
			}
			flags = append(flags, arg)
		} else {
			pos = append(pos, arg)
		}
	}
	return flags, pos, true
}

func promptConfigArguments(argv []string, s ioStreams) (int, bool) {
	flags, pos, ok := interactivePositionals(argv[1:], func(flag string) bool { return flag == "--debug" })
	if !ok || len(pos) == 0 {
		return 0, false
	}
	required := 2
	switch pos[0] {
	case "set":
		required = 3
	case "get", "unset":
	default:
		return 0, false
	}
	if len(pos) >= required {
		return 0, false
	}
	if code, blocked := guardRoot(s.err); blocked {
		return code, true
	}
	values := settings.EffectiveSettings(paths.GetBackupRoot())
	if len(pos) == 1 {
		labels := make([]string, len(values))
		for i, value := range values {
			labels[i] = value.Spec.Dotted() + "  " + settings.FormatSettingValue(value.Value)
		}
		i, _, ok := askMenu(s, "Choose a setting", labels, "")
		if !ok {
			return 130, true
		}
		pos = append(pos, values[i].Spec.Dotted())
	}
	if pos[0] == "set" {
		found := false
		for _, value := range values {
			if value.Spec.Dotted() != pos[1] {
				continue
			}
			found = true
			choices := value.Spec.Choices
			if value.Spec.Kind == settings.KindBool {
				choices = []string{"false", "true"}
			}
			initial := settings.FormatSettingValue(value.Value)
			if len(choices) > 0 {
				initial = ""
			} else {
				choices = nil
			}
			i, text, ok := askMenu(s, value.Spec.Label(), choices, initial)
			if !ok {
				return 130, true
			}
			if choices != nil {
				text = choices[i]
			}
			pos = append(pos, text)
			break
		}
		if !found {
			return 0, false
		}
	}
	args := append([]string{"config"}, flags...)
	return menuExecute(s, append(args, pos...)...), true
}
