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
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
)

var startExec = launch.Exec
var startResolve = launch.Resolve
var startResolveDefault = launch.ResolveDefault

// startSelect is the default start-login selection; tests watch it. It
// returns the start hold's token for the child, "" for none (DESIGN A105).
var startSelect = selectStartLogin

// startSelectMythos is the selection for a session on Claude Mythos: among
// the accounts marked for Mythos (DESIGN A106).
var startSelectMythos = func(account string, stderr io.Writer) string {
	return selectStartLoginFor(account, mythosFamily, stderr)
}

// startSelectFable is the start-login selection on the Fable store for a
// session that starts on it (DESIGN A105).
var startSelectFable = selectFableStartLogin

// startHoldEnv carries a start hold's token to the session's SessionStart
// hook, tycswap start --started (DESIGN A105).
const startHoldEnv = "TYCSWAP_START_HOLD"

// startFamilyEnv carries a Mythos session's family to its SessionStart hook,
// which records it before the session's first status line (DESIGN A106).
const startFamilyEnv = "TYCSWAP_START_FAMILY"

const mythosFamily = "mythos"

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
		case "--list", "--started":
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
	// The child gets env; this process acts on the default login under its locks.
	dropFableStoreEnv()
	parsed, err := parseStartArguments(argv)
	if err != nil {
		return subError(prog+" start", streams.err, err.Error())
	}
	if parsed.help {
		return renderStartHelp(prog, streams.out)
	}
	if parsed.action == "--started" {
		startStarted(streams.in)
		return 0
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
	childEnv, hold, family := env, "", ""
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
		// Every session starts on a start login of the default store, a
		// Fable-store session on one of the Fable store's too, under the
		// same token (DESIGN A105); a Mythos session on an account marked
		// for Mythos (A106).
		if startSessionFamily(args, env) == mythosFamily {
			family = mythosFamily
			hold = startSelectMythos(parsed.account, streams.err)
		} else {
			hold = startSelect(parsed.account, streams.err)
		}
		if onFable {
			fableHold, toDefault := startSelectFable(parsed.account, streams.err, hold)
			if toDefault {
				childEnv = withoutFableStore(env, storeDir)
			} else if fableHold != "" {
				hold = fableHold
			}
		}
	}
	childEnv = withStartVar(withStartHold(childEnv, hold), startFamilyEnv, family)
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

func selectStartLogin(account string, stderr io.Writer) (hold string) {
	return selectStartLoginFor(account, "", stderr)
}

// selectStartLoginFor is start's selection on the default login for a
// session of family: a Mythos session's candidates are the accounts marked
// for Mythos while any is, every other session's the start logins the
// default-session policy serves (DESIGN A106).
func selectStartLoginFor(account, family string, stderr io.Writer) (hold string) {
	if problem := startStoreProblem(paths.GetBackupRoot()); problem != "" {
		startNotice(stderr, problem)
		return ""
	}
	sw, err := newSwitcher(store.Options{OAuth: oauth.NewHTTPClient(), Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		startNotice(stderr, "Could not open the account store; launching on the current login.")
		return ""
	}
	if account != "" {
		data, readErr := sw.SequenceForUpdate()
		if readErr == nil {
			account, _, _, readErr = sw.ResolveAccountFrom(data, account)
		}
		if readErr != nil {
			reportStartSelection(sw, stderr, "Could not select the requested start login: "+readErr.Error()+"; launching on the current login.")
			return ""
		}
	}
	if family == mythosFamily && !anyMythosMark(sw) {
		reportStartSelection(sw, stderr, "no account is marked for Mythos (tycswap mythos --allow ID); selecting a start login as for any session.")
		family = ""
	}
	outcome := autoswitch.StartSelection(autoswitchAdapter{sw}, account, func() (autoswitch.StartSnapshot, error) {
		return readStartSnapshotFor(sw, account, family)
	}, autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(sw.OAuth), autoswitch.WithStatePath(sw.StatePath()))
	if outcome.Detail != "" {
		reportStartSelection(sw, stderr, outcome.Detail)
	}
	if outcome.Switched {
		sw.Log.Action("Switched to account "+outcome.To, "start")
	}
	return outcome.Hold
}

// anyMythosMark reports whether the roster marks an account for Mythos.
func anyMythosMark(sw *core.Switcher) bool {
	data, _ := sw.ReadSequence()
	if data == nil {
		return false
	}
	for _, n := range data.Sequence {
		if store.MythosFrom(data, strconv.Itoa(n)) {
			return true
		}
	}
	return false
}

// withStartHold is env with startHoldEnv set to token, or without it for "",
// so a session never inherits another's hold; env itself when unchanged.
func withStartHold(env []string, token string) []string {
	return withStartVar(env, startHoldEnv, token)
}

// withStartVar is env with name set to value, or without it for "", so a
// session never inherits another's; env itself when unchanged.
func withStartVar(env []string, name, value string) []string {
	out := env
	for i, entry := range env {
		if key, _, _ := strings.Cut(entry, "="); envKeyIs(key, name) {
			out = append([]string(nil), env[:i]...)
			for _, rest := range env[i+1:] {
				if key, _, _ := strings.Cut(rest, "="); !envKeyIs(key, name) {
					out = append(out, rest)
				}
			}
			break
		}
	}
	if value == "" {
		return out
	}
	return append(out[:len(out):len(out)], name+"="+value)
}

// selectFableStartLogin is start's selection on the Fable store: the marked
// accounts in the Fable pool (reporting.FablePool) are the candidates, or
// account alone; the Fable store's live account is the current one; the
// switch is the engine's Fable switch, stamped and held in the Fable state
// file under its lock, the hold under token when the default selection holds
// one. With none of them in the pool toDefault is true: the session starts on
// the default login. Without marks, or with auto-switch off and no account,
// it changes nothing (DESIGN A105).
func selectFableStartLogin(account string, stderr io.Writer, token string) (hold string, toDefault bool) {
	sw, err := newSwitcher(store.Options{OAuth: oauth.NewHTTPClient(), Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return "", false
	}
	a, err := newFableAdapter(sw, sw.Log)
	if err != nil {
		return "", false
	}
	if account != "" {
		data, readErr := sw.SequenceForUpdate()
		if readErr == nil {
			account, _, _, readErr = sw.ResolveAccountFrom(data, account)
		}
		if readErr != nil {
			return "", false
		}
	}
	snapshot := func() (autoswitch.StartSnapshot, error) { return readFableStartSnapshot(sw, account) }
	snap, err := snapshot()
	if err != nil {
		reportStartSelection(sw, stderr, "Fable store: Could not select a start login: "+err.Error()+"; continuing with the current login.")
		return "", false
	}
	if account == "" && (len(snap.Marked) == 0 || !snap.Settings.Enabled) {
		return "", false
	}
	if len(snap.Candidates) == 0 {
		reportStartSelection(sw, stderr, "no start login has a Fable login; starting on the default login.")
		return "", true
	}
	outcome := autoswitch.StartSelection(a, account, snapshot, autoswitch.WithClock(sw.Clk), autoswitch.WithOAuthClient(sw.OAuth),
		autoswitch.WithStatePath(a.fable.StatePath()), autoswitch.WithStartHoldToken(token))
	if outcome.Detail != "" {
		reportStartSelection(sw, stderr, "Fable store: "+outcome.Detail)
	}
	if outcome.Switched {
		sw.Log.Action("Switched the Fable store to account "+outcome.To, "start")
	}
	return outcome.Hold, false
}

func reportStartSelection(sw *core.Switcher, stderr io.Writer, detail string) {
	line := startNotice(stderr, detail)
	if sw.Log != nil {
		sw.Log.Infof("start: %s", line)
	}
}

func readStartSnapshot(sw *core.Switcher, account string) (autoswitch.StartSnapshot, error) {
	return readStartSnapshotFor(sw, account, "")
}

// readStartSnapshotFor is start's snapshot of the default login for a session
// of family: the start logins are the marked ones, for a Mythos session the
// accounts marked for Mythos, and only those the default-session policy
// serves are current or candidates (DESIGN A106).
func readStartSnapshotFor(sw *core.Switcher, account, family string) (autoswitch.StartSnapshot, error) {
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
	marked := store.StartLoginFrom
	if family == mythosFamily {
		marked = store.MythosFrom
	}
	snap := autoswitch.StartSnapshot{Settings: set}
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if marked(data, num) {
			snap.Marked = append(snap.Marked, num)
		}
	}
	if account == "" && (len(snap.Marked) == 0 || !set.Enabled) {
		return snap, nil
	}
	live := view.ResolveLiveSlot(data)
	snap.Current = live.Slot
	snap.CurrentMarked = marked(data, snap.Current)
	current := account == "" && snap.CurrentMarked || account != "" && snap.Current == account
	if current && live.Source == store.SourceConfig && view.AccountKindFor(snap.Current) == "oauth" {
		return snap, fmt.Errorf("current login ownership could not be verified from cached credentials; run tycswap status to reconcile it")
	}
	// With auto-switch off a current start login gets no start hold (DESIGN A105).
	if current && !set.Enabled {
		return snap, nil
	}
	policy := defaultSessionPolicy(view, set, view.Clk.Now())
	serves := func(num string) bool { return policy.Serves == nil || policy.Serves(num) }
	snap.Settings = policy.Settings
	snap.CurrentMarked = snap.CurrentMarked && serves(snap.Current)
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if (account == "" && marked(data, num) && serves(num) || account == num) && view.RotationEligible(data, num) {
			snap.Candidates = append(snap.Candidates, num)
		}
	}
	snap.Entries = (&core.Switcher{Store: view}).UsageEntriesByAccount(map[string]bool{})
	return snap, nil
}

// readFableStartSnapshot is start's snapshot of the Fable store: the live
// slot from backup or chain only, the candidates the marked accounts (or
// account) in the Fable pool, counted on the Fable windows as the Fable tick
// counts them. It makes no request.
func readFableStartSnapshot(sw *core.Switcher, account string) (autoswitch.StartSnapshot, error) {
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
		if num := strconv.Itoa(n); store.StartLoginFrom(data, num) {
			snap.Marked = append(snap.Marked, num)
		}
	}
	if account == "" && (len(snap.Marked) == 0 || !set.Enabled) {
		return snap, nil
	}
	pool := map[string]bool{}
	for _, num := range reporting.FablePool(view) {
		pool[num] = true
	}
	snap.Current = view.FableStore().Live.Slot
	snap.CurrentMarked = pool[snap.Current] && store.StartLoginFrom(data, snap.Current)
	snap.Settings.Model = nil
	if names := reporting.FableWindowNames(view, view.Clk.Now()); len(names) > 0 {
		model := strings.Join(names, ",")
		snap.Settings.Model = &model
	}
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if (account == "" && store.StartLoginFrom(data, num) || account == num) && pool[num] {
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
	fmt.Fprintf(out, "       %s start --started\n", prog)
	fmt.Fprintln(out, "\nStart on a marked login, then launch once (default: real Claude). A marked login with room")
	fmt.Fprintln(out, "comes first; one at its limit is still used, and auto-switch leaves it there until the session")
	fmt.Fprintln(out, "is up: --started, run as Claude Code's SessionStart hook, says so, else the hold ends after 30 s.")
	fmt.Fprintln(out, "Selection failures launch on the current login. Arguments pass unchanged.")
	fmt.Fprintln(out, "--account also works with auto-switch off; inherited auth/profile overrides skip selection.")
	fmt.Fprintln(out, "Sessions on a Fable model start on the Fable store when one is on (tycswap add --login --lineage fable),")
	fmt.Fprintln(out, "which moves to a marked login with a Fable login too; with marks but none such, they start on the default login.")
	fmt.Fprintln(out, "Sessions on Mythos start on an account marked with tycswap mythos --allow ID, while one is marked.")
	fmt.Fprintln(out, "The environment passes unchanged, except CLAUDE_SECURESTORAGE_CONFIG_DIR: start sets it to the Fable store")
	fmt.Fprintln(out, "for a Fable session while that store is usable, and removes it from any other session when it names that store;")
	fmt.Fprintln(out, "and TYCSWAP_START_HOLD: start sets it for a session it holds a login at its limit for, and removes it from any other.")
	return 0
}
