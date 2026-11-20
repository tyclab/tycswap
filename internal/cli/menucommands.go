package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
)

// A field is a positional input. An option is either a flag or flag=field.
// Only these static tokens are split; a person's input stays one argument.
type menuAction struct {
	command, fields, options, description string
}

const mythosSignInEntry = "add --login --lineage mythos  Sign in independently for Mythos"

var mythosEnrollment = menuAction{"add --login --lineage mythos", "--slot=MythosAccount", "--debug NativeArgs", "Enroll an independent Mythos login"}

var menuActions = []menuAction{
	{"list", "", "--json --token-status --debug", "List Claude accounts"},
	{"status", "", "--json --debug", "Show the active Claude account"},
	{"models --list", "", "--json", "List preferred models"},
	{"models", "Account Allocation", "", "Choose or clear preferred models"},
	{"add", "", "--slot=Slot --alias=Name --debug", "Save the current login"},
	{"add --login", "", "--slot=Slot --alias=Name --switch --debug NativeArgs", "Sign in to a new login"},
	{"add-token", "", "--slot=Slot --email=Email --base-url=URL --debug", "Enter a setup token or API key at the secure prompt"},
	{"alias", "", "--debug", "List aliases"},
	{"alias", "Account Name", "--debug", "Set an alias"},
	{"alias --unset", "Account", "--debug", "Clear an alias"},
	{"disable", "Account", "--debug", "Disable an account"},
	{"enable", "Account", "--debug", "Enable an account"},
	{"move", "Account Slot", "--debug", "Move an account to a slot"},
	{"swap", "Account OtherAccount", "--debug", "Exchange two slots"},
	{"remove", "Account", "--debug", "Remove an account"},
	{"switch", "", "--strategy=best|next-available --json --debug", "Rotate the default login"},
	{"switch", "Account", "--force --yes --json --debug", "Select the default login"},
	{"start", "", "--account=Account NativeArgs", "Start a new native process"},
	{"start --check", "", "", "Check a Claude status-line file against this login store"},
	{"start --list", "", "", "List startup accounts"},
	{"start --allow", "Account", "", "Mark a startup account"},
	{"start --disallow", "Account", "", "Remove a startup mark"},
	{"mythos --list", "", "", "List accounts marked for Mythos"},
	{"mythos --allow", "Account", "", "Record native Mythos access"},
	{"mythos --disallow", "Account", "", "Remove the Mythos mark"},
	{"fable --list", "", "--json", "List accounts marked for Fable"},
	{"fable --allow", "Account", "", "Record native Fable access"},
	{"fable --disallow", "Account", "", "Remove the Fable mark"},
	{"auto on", "", "--if-unset", "Enable automatic switching"},
	{"auto off", "", "", "Disable automatic switching"},
	{"auto status", "", "--json", "Show automatic switching status"},
	{"auto", "", "--once --dry-run --json --interval=Seconds --five-hour-threshold=Percent --seven-day-threshold=Percent --model-threshold=Percent --cooldown=Seconds --debug", "Run automatic switching"},
	{"config list", "", "--json --debug", "List settings"},
	{"config get", "Setting", "--json --debug", "Read a setting"},
	{"config set", "Setting Value", "--debug", "Change a setting"},
	{"config unset", "Setting", "--debug", "Reset a setting"},
	{"config path", "", "--debug", "Show the settings file path"},
	{"export", "ExportFile", "--account=Account --full --debug", "Export accounts to a file"},
	{"import", "ImportFile", "--force --debug", "Import accounts from a file"},
	{"tui", "", "--debug", "Open the terminal dashboard"},
	{"watch", "", "--debug", "Open the live watch page"},
	{"web", "", "--port=Port --no-open --interval=Seconds --debug", "Run the web dashboard"},
	{"web --reuse", "", "", "Open the running dashboard"},
	{"app", "", "--open --headless --port=Port --interval=Seconds --no-update-check --autostart=on|off|status --debug", "Run or configure the tray app"},
	{"app --remote", "URL", "--open --no-update-check --token-file=Path --autostart=on|off|status --debug", "Connect the tray to a remote dashboard"},
	{"upgrade", "", "--debug", "Install the latest release"},
	{"migrate", "", "--dry-run --json", "Import the old Claude store"},
	{"purge", "", "--debug", "Purge Claude data"},
	{"--version", "", "", "Print the version"},
	{"help", "", "", "Show CLI help"},
	{"codex --help", "", "", "Show all Codex account commands"},
}

func menuCommands(s ioStreams) {
	actions := slices.Clone(menuActions)
	if menuFableStore() {
		actions = append(actions, mythosEnrollment,
			menuAction{"switch --store fable --if-idle", "FableLogin", "--json --debug", "Select a saved login before starting Fable"},
			menuAction{"add --login --lineage fable", "", "--slot=Slot --debug NativeArgs", "Enroll an independent Fable login"},
			menuAction{"switch --store fable --empty", "", "--json --debug", "Turn the Fable store off"})
	}
	for _, verb := range codexVerbOrder {
		spec := codexVerbs[verb]
		action := menuAction{command: "codex " + verb, description: spec.usage}
		var fields, opts []string
		for _, field := range spec.required {
			switch field {
			case "A", "NUM|EMAIL|ALIAS", "NUM|EMAIL":
				fields = append(fields, "Account")
			case "B":
				fields = append(fields, "OtherAccount")
			case "PATH":
				if verb == "export" {
					fields = append(fields, "ExportFile")
				} else {
					fields = append(fields, "ImportFile")
				}
			case "SLOT":
				fields = append(fields, "Slot")
			}
		}
		switch verb {
		case "switch":
			actions = append(actions, menuAction{"codex switch", "Account", "--debug", "Select a specific Codex account"})
			action.description = "Rotate Codex accounts"
		case "alias":
			actions = append(actions, menuAction{"codex alias --unset", "Account", "--debug", "Clear a Codex alias"})
			fields = append(fields, "Name")
			action.description = "Set a Codex alias"
		}
		if verb != "alias" {
			opts = append(opts, spec.bools...)
		}
		for _, flag := range spec.values {
			field := map[string]string{"--strategy": "best", "--alias": "Name", "--account": "Account"}[flag]
			opts = append(opts, flag+"="+field)
		}
		action.fields, action.options = strings.Join(fields, " "), strings.Join(append(opts, "--debug"), " ")
		actions = append(actions, action)
	}
	labels := make([]string, len(actions))
	for i, action := range actions {
		labels[i] = action.command + "   " + action.description
	}
	i, _, ok := askMenu(s, "Choose a command", labels, "")
	if ok {
		runMenuAction(s, actions[i])
	}
}

func pickMenuAllocation(s ioStreams) (string, bool) {
	sets := []string{"--derived", "mythos", "fable", "opus", "mythos,fable", "mythos,opus", "fable,opus", "mythos,fable,opus"}
	labels := slices.Clone(sets)
	labels[0] = "Clear explicit allocation"
	i, _, ok := askMenu(s, "Preferred models", labels, "")
	if !ok {
		return "", false
	}
	return sets[i], true
}

type menuInvocation struct {
	args, tail []string
	accounts   []reporting.AccountSnapshot
}

func (cmd *menuInvocation) field(s ioStreams, name string) (string, bool) {
	switch name {
	case "Account", "OtherAccount", "MythosAccount", "FableLogin":
		provider := reporting.ProviderClaude
		if cmd.args[0] == "codex" {
			provider = reporting.ProviderCodex
		}
		var a reporting.AccountSnapshot
		var code int
		if name == "MythosAccount" || name == "FableLogin" {
			accounts, err := menuAccounts(s)
			if err != nil {
				fmt.Fprintln(s.err, err)
				return "", false
			}
			var eligible []reporting.AccountSnapshot
			fable := map[string]bool{}
			if name == "FableLogin" {
				rows, err := menuFableRows(s)
				if err != nil {
					fmt.Fprintln(s.err, err)
					return "", false
				}
				for _, row := range rows {
					fable[row.Number] = row.Login && row.Answer.Capable
				}
			}
			for _, row := range accounts {
				if row.ProviderName() == provider && !row.Disabled && row.Kind == "oauth" && (name == "MythosAccount" && row.Mythos || name == "FableLogin" && fable[row.Number]) {
					eligible = append(eligible, row)
				}
			}
			if len(eligible) == 0 {
				if name == "FableLogin" {
					fmt.Fprintln(s.err, "No enabled account has an eligible saved Fable login.")
				} else {
					fmt.Fprintln(s.err, "Independent sign-in needs an enabled Claude OAuth account marked for Mythos.")
				}
				return "", false
			}
			title := "Sign in independently as which account?"
			if name == "FableLogin" {
				title = "Use which saved Fable login?"
			}
			a, code = pickAccountRows(s, title, eligible)
		} else {
			title := "Choose the account"
			if name == "OtherAccount" {
				title = "Swap with which account?"
			}
			a, code = selectProviderAccount(s, title, provider)
		}
		if code != 0 {
			return "", false
		}
		cmd.accounts = append(cmd.accounts, a)
		return a.Number, true
	case "Allocation":
		return pickMenuAllocation(s)
	case "Setting":
		var keys []string
		for _, spec := range settings.SettingSpecs {
			keys = append(keys, spec.Dotted())
		}
		i, _, ok := askMenu(s, "Choose a setting", keys, "")
		if !ok {
			return "", false
		}
		return keys[i], true
	case "ExportFile", "ImportFile":
		return menuTransferFile(s, name == "ExportFile", "")
	}
	if strings.Contains(name, "|") || name == "best" {
		values := strings.Split(name, "|")
		i, _, ok := askMenu(s, "Choose a value", values, "")
		if !ok {
			return "", false
		}
		return values[i], true
	}
	_, text, ok := askMenu(s, name, nil, "")
	return text, ok && text != ""
}

func runMenuAction(s ioStreams, action menuAction) {
	command := strings.Fields(action.command)
	options := strings.Fields(action.options)
	chosen := map[string]bool{}
	cmd := menuInvocation{args: command}
	var optional []string
	for {
		choices := []string{strings.TrimSpace("Run " + action.command + " " + strings.Join(optional, " ")), "Command help"}
		for _, option := range options {
			if !chosen[option] {
				label := option
				if option == "NativeArgs" {
					label = "--   Pass arguments to the native command…"
				}
				choices = append(choices, label)
			}
		}
		i, _, ok := askMenu(s, "Options for "+action.command, choices, "")
		if !ok {
			return
		}
		switch i {
		case 0:
			for _, name := range strings.Fields(action.fields) {
				if flag, field, named := strings.Cut(name, "="); named {
					cmd.args = append(cmd.args, flag)
					name = field
				}
				value, ok := cmd.field(s, name)
				if !ok {
					return
				}
				cmd.args = append(cmd.args, value)
			}
			for _, account := range cmd.accounts {
				if !menuAccountCurrent(s, account) {
					return
				}
			}
			if action.command == "start --check" {
				file, ok := menuStatusFile(s)
				if !ok {
					return
				}
				defer file.Close()
				s.in = file
			}
			args := append(cmd.args, optional...)
			menuExecute(s, append(args, cmd.tail...)...)
			return
		case 1:
			verb := command[:1:1]
			if command[0] == "codex" {
				verb = command[:2:2]
			}
			menuExecute(s, append(verb, "--help")...)
			continue
		}
		opt := choices[i]
		if strings.HasPrefix(opt, "--   ") {
			opt = "NativeArgs"
		}
		chosen[opt] = true
		key, field, hasValue := strings.Cut(opt, "=")
		switch {
		case key == "NativeArgs":
			var tail []string
			for {
				_, arg, ok := askMenu(s, "Native argument (blank finishes, one argument per entry)", nil, "")
				if !ok {
					return
				}
				if arg == "" {
					break
				}
				tail = append(tail, arg)
			}
			if len(tail) > 0 {
				cmd.tail = append([]string{"--"}, tail...)
			}
		case hasValue || !strings.HasPrefix(key, "-"):
			if !hasValue {
				field = key
			}
			value, ok := cmd.field(s, field)
			if !ok {
				return
			}
			if hasValue {
				optional = append(optional, key)
			}
			optional = append(optional, value)
		default:
			optional = append(optional, key)
		}
	}
}
