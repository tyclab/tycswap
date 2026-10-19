package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/termsafe"
)

func menuFilePath(s ioStreams, title, initial string) (string, bool) {
	_, path, ok := askMenu(s, title, nil, initial)
	if !ok || strings.TrimSpace(path) == "" {
		return "", false
	}
	if path == "-" {
		fmt.Fprintln(s.err, "Choose a file path for the interactive menu.")
		return "", false
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(s.err, err)
			return "", false
		}
		if len(path) > 1 {
			path = filepath.Join(home, path[2:])
		} else {
			path = home
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintln(s.err, err)
		return "", false
	}
	return abs, true
}

func menuTransfer(s ioStreams) {
	operation, _, ok := askMenu(s, "Import / export", []string{"Export accounts…", "Import accounts…"}, "")
	if !ok {
		return
	}
	provider, _, ok := askMenu(s, "Which accounts?", []string{"Claude", "Codex"}, "")
	if !ok {
		return
	}
	args := []string{}
	name := reporting.ProviderClaude
	if provider == 1 {
		name = reporting.ProviderCodex
		args = append(args, "codex")
	}
	var selected *reporting.AccountSnapshot
	if operation == 0 {
		args = append(args, "export")
		scope, _, ok := askMenu(s, "Export which accounts?", []string{"All " + name + " accounts", "Choose one account…"}, "")
		if !ok {
			return
		}
		if scope == 1 {
			a, code := selectProviderAccount(s, "Export which account?", name)
			if code != 0 {
				return
			}
			selected = &a
			args = append(args, "--account", a.Number)
		}
	} else {
		args = append(args, "import")
	}
	initial, title := "", "File to import"
	if operation == 0 {
		initial, title = name+"-accounts.tycswap", "Save export to"
	}
	path, ok := menuFilePath(s, title, initial)
	if !ok {
		return
	}
	if operation == 0 {
		if _, err := os.Lstat(path); err == nil {
			fmt.Fprintln(s.err, "That path already exists. Choose a new export file.")
			return
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(s.err, err)
			return
		}
		if !menuConfirm(s, "Export to "+termsafe.Strip(path)+"? The file contains account tokens.") {
			return
		}
	} else {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			fmt.Fprintln(s.err, "Choose an existing account export file.")
			return
		}
		mode, _, ok := askMenu(s, "Accounts already saved here", []string{"Keep existing accounts", "Replace matching accounts"}, "")
		if !ok {
			return
		}
		if mode == 1 {
			args = append(args, "--force")
		}
		if !menuConfirm(s, "Import accounts from "+termsafe.Strip(path)+"?") {
			return
		}
	}
	if selected != nil && !menuAccountCurrent(s, *selected) {
		return
	}
	menuExecute(s, append(args, path)...)
}
