package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/termsafe"
	"github.com/tyclab/tycswap/internal/web"
)

var menuGroupViews = func(s ioStreams) ([]web.GroupView, error) {
	sw, err := constructSwitcher(false, s.err)
	if err != nil {
		return nil, err
	}
	return (groupFacade{sw}).Views(), nil
}

var menuSessionExecute = func(s ioStreams, dir string, args ...string) int {
	fmt.Fprintln(s.out, "Working directory: "+termsafe.Strip(dir))
	showMenuCommand(s, args)
	cmd := exec.Command(exePath(), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TYCSWAP_NO_PROMPT=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, s.out, s.err
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(s.err, err)
		return 1
	}
	return 0
}

func menuGroups(s ioStreams) {
	views, err := menuGroupViews(s)
	if err != nil {
		fmt.Fprintln(s.err, err)
		return
	}
	var groups []web.GroupView
	var labels []string
	for _, view := range views {
		if view.ID == "" {
			fmt.Fprintln(s.err, termsafe.Strip(view.Blocker))
			continue
		}
		account := "no account"
		if view.ActiveNumber != "" {
			account = "account #" + view.ActiveNumber
		}
		groups = append(groups, view)
		labels = append(labels, fmt.Sprintf("%s · %s · %d sessions", view.Label, account, view.LiveSessions))
	}
	if len(groups) == 0 {
		return
	}
	i, _, ok := askMenu(s, "Session groups", labels, "")
	if !ok {
		return
	}
	g := groups[i]
	if g.Blocker != "" {
		fmt.Fprintln(s.err, termsafe.Strip(g.Blocker))
	}
	choices := []string{"Switch account…", "Start session…", "Resume session…", "Group settings…", "Reconcile ownership"}
	if g.ID == "fable" {
		choices = append(choices, "Account compatibility…")
	}
	action, _, ok := askMenu(s, g.Label, choices, "")
	if !ok {
		return
	}
	switch action {
	case 0:
		a, code := selectProviderAccount(s, "Account for "+g.Label, reporting.ProviderClaude)
		if code != 0 {
			return
		}
		if reason := g.AccountBlockers[a.Number]; reason != "" {
			fmt.Fprintln(s.err, termsafe.Strip(reason))
			return
		}
		if menuConfirm(s, "Switch "+g.Label+" to "+termsafe.Strip(a.Email)+"? Sessions in this group switch together.") && menuAccountCurrent(s, a) {
			menuExecute(s, "groups", "switch", g.ID, a.Number)
		}
	case 1, 2:
		menuGroupSession(s, g, action == 2)
	case 3:
		menuGroupSettings(s, g)
	case 4:
		menuExecute(s, "groups", "reconcile", g.ID)
	case 5:
		a, code := selectProviderAccount(s, "Account compatibility", reporting.ProviderClaude)
		if code != 0 {
			return
		}
		intent, _, ok := askMenu(s, "Fable compatibility", []string{"Start new sessions", "Continue existing sessions"}, "")
		if !ok {
			return
		}
		allowed, _, ok := askMenu(s, "Has this account's compatibility been confirmed?", []string{"Not supported", "Supported"}, "")
		if ok && menuAccountCurrent(s, a) {
			menuExecute(s, "groups", "capability", a.Number, []string{"start", "continue"}[intent], []string{"false", "true"}[allowed])
		}
	}
}

func menuGroupSession(s ioStreams, g web.GroupView, resume bool) {
	args := []string{"run", "--group", g.ID}
	var selected *reporting.AccountSnapshot
	if g.ActiveNumber == "" {
		a, code := selectProviderAccount(s, "First account for "+g.Label, reporting.ProviderClaude)
		if code != 0 || !menuAccountCurrent(s, a) {
			return
		}
		args = append(args, a.Number)
		selected = &a
	}
	models := []string{"opus", "sonnet", "haiku"}
	if g.ID == "fable" {
		models = []string{"fable"}
	}
	i, _, ok := askMenu(s, "Session model", models, "")
	if !ok {
		return
	}
	args = append(args, "--model", models[i])
	if resume {
		_, id, ok := askMenu(s, "Session ID or transcript path", nil, "")
		if !ok || id == "" {
			return
		}
		args = append(args, "--resume", id)
	}
	cwd, _ := os.Getwd()
	dir, ok := menuFilePath(s, "Project directory", cwd)
	if !ok {
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(s.err, "Choose an existing project directory.")
		return
	}
	if selected != nil && !menuAccountCurrent(s, *selected) {
		return
	}
	menuSessionExecute(s, dir, args...)
}

func menuGroupSettings(s ioStreams, g web.GroupView) {
	var editable []web.SettingView
	var labels []string
	for _, view := range g.SettingViews {
		if view.ReadOnly == "" {
			editable = append(editable, view)
			labels = append(labels, view.Label+": "+settings.FormatSettingValue(view.Value))
		}
	}
	if len(editable) == 0 {
		return
	}
	i, _, ok := askMenu(s, g.Label+" settings", labels, "")
	if !ok {
		return
	}
	v := editable[i]
	args := []string{"groups", "config", g.ID}
	if len(v.Choices) > 0 {
		choices := append(append([]string{}, v.Choices...), "Use shared default")
		i, _, ok := askMenu(s, v.Label, choices, "")
		if !ok {
			return
		}
		if i == len(v.Choices) {
			menuExecute(s, append(args, "--unset", v.Key)...)
		} else {
			menuExecute(s, append(args, v.Key, choices[i])...)
		}
		return
	}
	_, raw, ok := askMenu(s, v.Label+" (empty uses shared default)", nil, settings.FormatSettingValue(v.Value))
	if !ok {
		return
	}
	if raw == "" {
		menuExecute(s, append(args, "--unset", v.Key)...)
	} else {
		menuExecute(s, append(args, v.Key, raw)...)
	}
}
