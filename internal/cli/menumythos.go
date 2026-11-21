package cli

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
)

func menuMythosAccount(s ioStreams, forSwitch bool) (reporting.AccountSnapshot, int) {
	accounts, err := menuAccounts(s)
	if err != nil {
		fmt.Fprintln(s.err, err)
		return reporting.AccountSnapshot{}, 1
	}
	var ready []reporting.AccountSnapshot
	for _, a := range accounts {
		if a.ProviderName() != reporting.ProviderClaude || a.Disabled || !a.Mythos || a.Kind != "oauth" || a.MythosLogin == nil || a.MythosLogin.Status != reporting.FableLoginOK {
			continue
		}
		if !forSwitch || a.MythosLogin.Selectable {
			ready = append(ready, a)
		}
	}
	if len(ready) == 0 {
		fmt.Fprintln(s.err, "No eligible independent Mythos login is available for this action.")
		return reporting.AccountSnapshot{}, 1
	}
	title := "Start Mythos as which account?"
	if forSwitch {
		title = "Use which Mythos login?"
	}
	return pickAccountRows(s, title, ready)
}

func menuStartMythos(s ioStreams) (int, bool) {
	a, code := menuMythosAccount(s, false)
	if code != 0 {
		return code, false
	}
	args := []string{"--mythos", "--account", a.Number}
	showMenuCommand(s, append([]string{"start"}, args...))
	expected := store.MythosLoginDir(paths.GetBackupRoot(), a.Email, a.OrgUUID)
	return startCommandFor("tycswap", args, s, expected), true
}

func menuMoveSession(s ioStreams, wanted string) (string, []string, bool) {
	sw, err := constructSwitcher(false, s.err)
	if err != nil {
		fmt.Fprintln(s.err, err)
		return "", nil, false
	}
	sessions, err := procdetect.ListSessionsErr(sw.DefaultProfileDir())
	if err != nil {
		fmt.Fprintln(s.err, err)
		return "", nil, false
	}
	var labels, ids []string
	var destinations [][]string
	for _, session := range sessions {
		id := session.SessionID
		if !sessionUUID.MatchString(id) || slices.Contains(ids, id) {
			continue
		}
		options, err := sessionMoveChoices(sw, id)
		if err != nil {
			if cerr.TypeName(err) != string(cerr.KindAccountNotFound) {
				fmt.Fprintf(s.err, "%s: %v\n", id, err)
			}
		}
		if len(options) == 0 || (wanted != "" && !slices.Contains(options, wanted)) {
			continue
		}
		label := id
		if session.CWD != "" {
			label += "  " + filepath.Clean(session.CWD)
		}
		labels = append(labels, termsafe.Strip(label))
		ids = append(ids, id)
		destinations = append(destinations, options)
	}
	if len(labels) == 0 {
		fmt.Fprintln(s.err, "No running linked session is available for this move.")
		return "", nil, false
	}
	i, _, ok := askMenu(s, "Move which session?", labels, "")
	if !ok {
		return "", nil, false
	}
	return ids[i], destinations[i], true
}

func menuSessionDestination(s ioStreams) {
	id, options, ok := menuMoveSession(s, "")
	if !ok {
		return
	}
	labels := make([]string, len(options))
	for i, target := range options {
		switch target {
		case "mythos":
			labels[i] = "Move to Mythos login"
		case "fable":
			labels[i] = "Move to Fable store"
		case "default":
			labels[i] = "Move to default login"
		}
	}
	i, _, ok := askMenu(s, "Move session "+id, labels, "")
	if ok {
		menuExecute(s, "use", options[i], "--session", id)
	}
}
