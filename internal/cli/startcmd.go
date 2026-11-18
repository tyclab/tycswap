package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

var startExec = launch.Exec
var startResolve = launch.Resolve
var startResolveDefault = launch.ResolveDefault

// startSelect is the default start-login selection; tests watch it.
var startSelect = selectStartLogin

type startArguments struct {
	account, action, id string
	command             []string
	help                bool
}

func parseStartArguments(argv []string) (startArguments, error) {
	var parsed startArguments
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if tok == "--" {
			parsed.command = argv[i+1:]
			if parsed.action != "" {
				return parsed, fmt.Errorf("management options cannot launch a command")
			}
			break
		}
		switch tok {
		case "--allow", "--disallow", "--account":
			if i+1 == len(argv) || argv[i+1] == "" || strings.HasPrefix(argv[i+1], "--") {
				return parsed, fmt.Errorf("%s requires an account identifier", tok)
			}
			i++
			if tok == "--account" {
				if parsed.account != "" || parsed.action != "" {
					return parsed, fmt.Errorf("--account cannot be combined with another account or management option")
				}
				parsed.account = argv[i]
			} else {
				if parsed.action != "" || parsed.account != "" {
					return parsed, fmt.Errorf("management options are exclusive")
				}
				parsed.action, parsed.id = tok, argv[i]
			}
		case "--list":
			if parsed.action != "" || parsed.account != "" {
				return parsed, fmt.Errorf("management options are exclusive")
			}
			parsed.action = tok
		case "-h", "--help":
			parsed.help = true
		default:
			if strings.HasPrefix(tok, "-") {
				return parsed, fmt.Errorf("unrecognized option: %s", tok)
			}
			if parsed.action != "" {
				return parsed, fmt.Errorf("management options cannot launch a command")
			}
			parsed.command = argv[i:]
			return parsed, nil
		}
	}
	return parsed, nil
}

func startCommand(prog string, argv []string, streams ioStreams) int {
	env := os.Environ()
	parsed, err := parseStartArguments(argv)
	if err != nil {
		return subError(prog+" start", streams.err, err.Error())
	}
	if parsed.help {
		return renderStartHelp(prog, streams.out)
	}
	if parsed.action != "" {
		return startManagement(parsed, streams)
	}
	var bin string
	if len(parsed.command) == 0 {
		bin, err = startResolveDefault()
	} else {
		bin, err = startResolve(parsed.command[0])
	}
	if err != nil {
		startNotice(streams.err, "Could not launch command: "+err.Error())
		return launch.ExitCode(err)
	}
	args := []string{bin}
	if len(parsed.command) > 0 {
		args = parsed.command
	}
	if err := launch.CheckCmdShimArgs(bin, args[1:]); err != nil {
		startNotice(streams.err, "Could not launch command: "+err.Error())
		return 1
	}
	root := paths.GetBackupRoot()
	storeDir := filepath.Clean(paths.FableStoreDir(root))
	childEnv := env
	if reason := startOverride(env, parsed.command, storeDir); reason != "" {
		if parsed.account != "" {
			startNotice(streams.err, "Skipping the requested start login because "+reason+"; launching unchanged.")
		}
	} else {
		fable, hasFableLogins := startFableStore(root)
		var onFable bool
		var notice string
		childEnv, onFable, notice = startEnvironment(env, args, fable, hasFableLogins, storeDir)
		if notice != "" {
			startNotice(streams.err, notice)
		}
		if !onFable || parsed.account != "" {
			startSelect(parsed.account, streams.err)
		}
		if onFable && parsed.account != "" {
			startNotice(streams.err, "--account selects the default login; this Fable session starts on the Fable store"+accountSuffix(fable.Live.Slot)+".")
		}
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	release := claimSigint()
	defer release()
	code, err := startExec(bin, args, childEnv)
	if err != nil {
		startNotice(streams.err, "Could not launch command: "+err.Error())
	}
	return code
}

func startOverride(env, command []string, storeDir string) string {
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if envKeyIs(key, "CLAUDE_CONFIG_DIR") {
			return key + " is set"
		}
		if envKeyIs(key, secureStorageEnv) && namesFableStore(value, storeDir, startWindows()) {
			continue
		}
		for _, override := range launch.AuthOverrideEnvVars {
			if envKeyIs(key, override) {
				return override + " is set"
			}
		}
	}
	args := command
	if len(args) > 0 {
		args = args[1:]
	}
	for _, arg := range args {
		if arg == "--bare" {
			return "the command uses --bare"
		}
	}
	return ""
}

// startStoreProblem is why start must not read the tycswap store at root, as
// a notice; "" when it may.
func startStoreProblem(root string) string {
	const fallback = "; launching on the current login."
	if info, err := os.Stat(root); err == nil {
		if !info.IsDir() {
			return "Could not select a start login: the tycswap store " + root + " is not a directory" + fallback
		}
	} else if !os.IsNotExist(err) {
		return "Could not select a start login: could not inspect the tycswap store: " + err.Error() + fallback
	}
	warning, err := paths.CheckPrivateRoot(root)
	if err != nil {
		return "Could not select a start login: " + err.Error() + fallback
	}
	if warning != "" {
		return warning + fallback
	}
	if geteuid() == 0 && !platform.IsWindows() && !platform.RunningInContainer() {
		return "Start login selection is unavailable as root" + fallback
	}
	return ""
}

func selectStartLogin(account string, stderr io.Writer) {
	if problem := startStoreProblem(paths.GetBackupRoot()); problem != "" {
		startNotice(stderr, problem)
		return
	}
	sw, err := newSwitcher(store.Options{OAuth: oauth.NewHTTPClient(), Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		startNotice(stderr, "Could not open the account store; launching on the current login.")
		return
	}
	if account != "" {
		data, readErr := sw.SequenceForUpdate()
		if readErr == nil {
			account, _, _, readErr = sw.ResolveAccountFrom(data, account)
		}
		if readErr != nil {
			reportStartSelection(sw, stderr, "Could not select the requested start login: "+readErr.Error()+"; launching on the current login.")
			return
		}
	}
	outcome := autoswitch.StartSelection(autoswitchAdapter{sw}, account, func() (autoswitch.StartSnapshot, error) {
		return readStartSnapshot(sw, account)
	}, autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(sw.OAuth), autoswitch.WithStatePath(sw.StatePath()))
	if outcome.Detail != "" {
		reportStartSelection(sw, stderr, outcome.Detail)
	}
	if outcome.Switched {
		sw.Log.Action("Switched to account "+outcome.To, "start")
	}
}

func reportStartSelection(sw *core.Switcher, stderr io.Writer, detail string) {
	line := startNotice(stderr, detail)
	if sw.Log != nil {
		sw.Log.Infof("start: %s", line)
	}
}

func readStartSnapshot(sw *core.Switcher, account string) (autoswitch.StartSnapshot, error) {
	view := sw.Store.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	data, err := view.SequenceForUpdate()
	if err != nil {
		return autoswitch.StartSnapshot{}, err
	}
	set, err := loadStartSettings(view.BackupDir())
	if err != nil {
		return autoswitch.StartSnapshot{}, err
	}
	snap := autoswitch.StartSnapshot{Settings: set}
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if store.StartLoginFrom(data, num) {
			snap.Marked = append(snap.Marked, num)
		}
	}
	if account == "" && (len(snap.Marked) == 0 || !set.Enabled) {
		return snap, nil
	}
	live := view.ResolveLiveSlot(data)
	snap.Current = live.Slot
	snap.CurrentMarked = store.StartLoginFrom(data, snap.Current)
	if account == "" && snap.CurrentMarked || account != "" && snap.Current == account {
		if live.Source == store.SourceConfig && view.AccountKindFor(snap.Current) == "oauth" {
			return snap, fmt.Errorf("current login ownership could not be verified from cached credentials; run tycswap status to reconcile it")
		}
		return snap, nil
	}
	snap.Settings = defaultSessionPolicy(view, set, view.Clk.Now()).Settings
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if (account == "" && store.StartLoginFrom(data, num) || account == num) && view.RotationEligible(data, num) {
			snap.Candidates = append(snap.Candidates, num)
		}
	}
	snap.Entries = (&core.Switcher{Store: view}).UsageEntriesByAccount(map[string]bool{})
	return snap, nil
}

func loadStartSettings(root string) (settings.AutoSwitchSettings, error) {
	raw, err := os.ReadFile(settings.SettingsPath(root))
	if os.IsNotExist(err) {
		return settings.Default(), nil
	}
	if err != nil {
		return settings.AutoSwitchSettings{}, fmt.Errorf("settings.json could not be read: %w", err)
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return settings.AutoSwitchSettings{}, fmt.Errorf("settings.json is not a JSON object; repair it before selecting a start login")
	}
	if section, present := doc["autoswitch"]; present {
		if _, ok := section.(map[string]any); !ok {
			return settings.AutoSwitchSettings{}, fmt.Errorf("settings.json autoswitch is not an object; repair it before selecting a start login")
		}
	}
	return settings.Load(root), nil
}

func startManagement(args startArguments, streams ioStreams) int {
	warning, err := paths.CheckPrivateRoot(paths.GetBackupRoot())
	if err != nil {
		startNotice(streams.err, err.Error())
		return 1
	}
	if warning != "" {
		startNotice(streams.err, warning)
	}
	if code, blocked := guardRoot(streams.err); blocked {
		return code
	}
	sw, err := constructSwitcher(false, streams.err)
	if err != nil {
		startNotice(streams.err, err.Error())
		return 1
	}
	if args.action == "--list" {
		data, err := sw.SequenceForUpdate()
		if err != nil {
			startNotice(streams.err, err.Error())
			return 1
		}
		for _, n := range data.Sequence {
			if num := strconv.Itoa(n); store.StartLoginFrom(data, num) {
				fmt.Fprintln(streams.out, num)
			}
		}
		return 0
	}
	if err := sw.SetStartLogin(args.id, args.action == "--allow"); err != nil {
		startNotice(streams.err, err.Error())
		return 1
	}
	return 0
}

func startNotice(stderr io.Writer, message string) string {
	line := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	fmt.Fprintln(stderr, "tycswap start: "+line)
	return line
}

func renderStartHelp(prog string, out io.Writer) int {
	fmt.Fprintf(out, "usage: %s start [--account ID] [--] [command [args...]]\n", prog)
	fmt.Fprintf(out, "       %s start --allow ID | --disallow ID | --list\n", prog)
	fmt.Fprintln(out, "\nStart on a usable marked login, then launch once (default: real Claude).")
	fmt.Fprintln(out, "Selection failures launch on the current login. Arguments pass unchanged.")
	fmt.Fprintln(out, "--account also works with auto-switch off; inherited auth/profile overrides skip selection.")
	fmt.Fprintln(out, "Sessions on a Fable model start on the Fable store when one is on (tycswap add --login --lineage fable).")
	fmt.Fprintln(out, "The environment passes unchanged, except CLAUDE_SECURESTORAGE_CONFIG_DIR: start sets it to the Fable store")
	fmt.Fprintln(out, "for a Fable session while that store is usable, and removes it from any other session when it names that store.")
	return 0
}
