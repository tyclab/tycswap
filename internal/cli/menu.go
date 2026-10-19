package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/providers"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/termsafe"
)

func humanTerminal(stdinTTY, stdoutTTY bool) bool {
	return stdinTTY && stdoutTTY && os.Getenv("TERM") != "dumb" &&
		os.Getenv("TYCSWAP_NO_PROMPT") == "" && os.Getenv("CLAUDECODE") == "" && os.Getenv("CODEX_THREAD_ID") == ""
}

var menuExecute = func(s ioStreams, args ...string) int {
	cmd := exec.Command(exePath(), args...)
	cmd.Env = append(os.Environ(), "TYCSWAP_NO_PROMPT=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, s.out, s.err
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		fmt.Fprintln(s.err, err)
		return 1
	}
	return 0
}

var menuAccounts = func(s ioStreams) ([]reporting.AccountSnapshot, error) {
	sw, err := constructSwitcher(false, s.err)
	if err != nil {
		return nil, err
	}
	src := providers.NewMultiSnapshotSource(context.Background(), sw, newQuietCodexSwitcher())
	snapshot := src.AccountsSnapshot(map[string]bool{})
	if snapshot == nil {
		return nil, fmt.Errorf("accounts are unavailable")
	}
	return snapshot.Accounts, nil
}

var runHumanMenu = humanMenu

func menuCommand(prog string, args []string, s ioStreams, terminal bool) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintf(s.out, "usage: %s menu [add]\n\nChoose an action interactively. Escape goes back; closing the menu leaves the tray running.\n", prog)
		return 0
	}
	if len(args) > 1 || len(args) == 1 && args[0] != "add" {
		return subError(prog+" menu", s.err, "expected no argument or add")
	}
	if !terminal {
		return subError(prog+" menu", s.err, "an interactive terminal is required")
	}
	start := ""
	if len(args) == 1 {
		start = args[0]
	}
	return runHumanMenu(prog, start, s)
}

func humanMenu(prog, start string, s ioStreams) int {
	if code, blocked := guardRoot(s.err); blocked {
		return code
	}
	if start == "add" {
		menuAdd(s)
	}
	for {
		i, _, ok := askMenu(s, "tycswap", []string{
			"Add account…", "Manage accounts…", "Live dashboard", "Open web dashboard",
			"Advanced settings…", "Start tray app", "Close menu",
		}, "")
		if !ok || i == 6 {
			return 0
		}
		switch i {
		case 0:
			menuAdd(s)
		case 1:
			menuManage(s)
		case 2:
			menuExecute(s, "tui")
		case 3:
			menuExecute(s, "web")
		case 4:
			menuSettings(s)
		case 5:
			startBackgroundApp(prog, s)
		}
	}
}

func menuAdd(s ioStreams) {
	i, _, ok := askMenu(s, "Add account", []string{
		"Claude — sign in with the browser", "Codex — sign in with the browser",
		"Save current Claude login", "Save current Codex login", "Claude — setup token or API key",
	}, "")
	if !ok {
		return
	}
	switch i {
	case 0:
		menuExecute(s, "add", "--login")
	case 1:
		fmt.Fprintln(s.err, "Codex sign-in changes the current Codex login. Existing sessions keep their account until restarted.")
		if menuConfirm(s, "Continue with Codex sign-in?") {
			menuExecute(s, "codex", "login")
		}
	case 2:
		menuExecute(s, "add")
	case 3:
		menuExecute(s, "codex", "add")
	case 4:
		_, email, ok := askMenu(s, "Account email (optional)", nil, "")
		if !ok {
			return
		}
		_, baseURL, ok := askMenu(s, "API endpoint (optional)", nil, "")
		if !ok {
			return
		}
		args := []string{"add-token"}
		if email != "" {
			args = append(args, "--email", email)
		}
		if baseURL != "" {
			args = append(args, "--base-url", baseURL)
		}
		menuExecute(s, args...)
	}
}

func menuConfirm(s ioStreams, question string) bool {
	i, _, ok := askMenu(s, question, []string{"Cancel", "Continue"}, "")
	return ok && i == 1
}

func pickMenuAccount(s ioStreams, title string) (reporting.AccountSnapshot, bool) {
	accounts, err := menuAccounts(s)
	if err != nil {
		fmt.Fprintln(s.err, err)
		return reporting.AccountSnapshot{}, false
	}
	if len(accounts) == 0 {
		fmt.Fprintln(s.err, "No accounts yet. Choose Add account to sign in.")
		return reporting.AccountSnapshot{}, false
	}
	labels := make([]string, len(accounts))
	for i, a := range accounts {
		label := fmt.Sprintf("%s #%s  %s", a.ProviderName(), a.Number, a.Email)
		if a.Alias != "" {
			label += "  (" + a.Alias + ")"
		}
		if a.OrgName != "" {
			label += "  [" + a.OrgName + "]"
		}
		if a.IsActive {
			label += " · active"
		}
		if a.Disabled {
			label += " · disabled"
		}
		labels[i] = termsafe.Strip(label)
	}
	i, _, ok := askMenu(s, title, labels, "")
	if !ok {
		return reporting.AccountSnapshot{}, false
	}
	return accounts[i], true
}

func menuAccountCurrent(s ioStreams, selected reporting.AccountSnapshot) bool {
	accounts, err := menuAccounts(s)
	if err == nil {
		for _, a := range accounts {
			if a.Key() == selected.Key() && a.Email == selected.Email && a.OrgUUID == selected.OrgUUID && a.OrgName == selected.OrgName {
				return true
			}
		}
	}
	fmt.Fprintln(s.err, "The account changed while the menu was open. Choose it again.")
	return false
}

func menuManage(s ioStreams) {
	a, ok := pickMenuAccount(s, "Manage which account?")
	if !ok {
		return
	}
	toggle := "Disable automatic rotation"
	if a.Disabled {
		toggle = "Enable automatic rotation"
	}
	i, _, ok := askMenu(s, termsafe.Strip(a.Email), []string{"Switch to this account", "Rename…", toggle, "Move to another slot…", "Remove stored account"}, "")
	if !ok {
		return
	}
	args := []string{}
	if a.ProviderName() == reporting.ProviderCodex {
		args = append(args, "codex")
	}
	switch i {
	case 0:
		if !menuConfirm(s, "Switch to "+termsafe.Strip(a.Email)+"?") {
			return
		}
		args = append(args, "switch", a.Number)
	case 1:
		_, name, ok := askMenu(s, "Account name (empty clears it)", nil, a.Alias)
		if !ok {
			return
		}
		args = append(args, "alias", a.Number)
		if strings.TrimSpace(name) == "" {
			args = append(args, "--unset")
		} else {
			args = append(args, name)
		}
	case 2:
		verb := "disable"
		if a.Disabled {
			verb = "enable"
		}
		args = append(args, verb, a.Number)
	case 3:
		_, slot, ok := askMenu(s, "Destination slot", nil, "")
		if !ok || slot == "" {
			return
		}
		if !menuConfirm(s, "Move to slot "+termsafe.Strip(slot)+"? An occupied slot will be swapped.") {
			return
		}
		args = append(args, "move", a.Number, slot)
	case 4:
		args = append(args, "remove", a.Number)
	}
	if menuAccountCurrent(s, a) {
		menuExecute(s, args...)
	}
}

func menuSettings(s ioStreams) {
	values := settings.EffectiveSettings(paths.GetBackupRoot())
	labels := make([]string, len(values))
	for i, value := range values {
		labels[i] = value.Spec.Label() + ": " + settings.FormatSettingValue(value.Value)
	}
	i, _, ok := askMenu(s, "Advanced auto-switch settings", labels, "")
	if !ok {
		return
	}
	spec := values[i].Spec
	if spec.Kind == settings.KindBool || spec.Kind == settings.KindChoice {
		choices := append([]string(nil), spec.Choices...)
		if spec.Kind == settings.KindBool {
			choices = []string{"false", "true"}
		}
		choices = append(choices, "Reset to default")
		selected, _, ok := askMenu(s, spec.Label(), choices, "")
		if !ok {
			return
		}
		if selected == len(choices)-1 {
			menuExecute(s, "config", "unset", spec.Dotted())
		} else {
			menuExecute(s, "config", "set", spec.Dotted(), choices[selected])
		}
		return
	}
	_, value, ok := askMenu(s, spec.Help+" (empty resets)", nil, settings.FormatSettingValue(values[i].Value))
	if !ok {
		return
	}
	if value == "" {
		menuExecute(s, "config", "unset", spec.Dotted())
	} else {
		menuExecute(s, "config", "set", spec.Dotted(), value)
	}
}
