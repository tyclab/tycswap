package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
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
var startSelect = selectStartLoginFor

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
	help, mythos        bool
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
		case "--mythos":
			if parsed.mythos {
				return parsed, fmt.Errorf("--mythos may be given only once")
			}
			parsed.mythos = true
		case "--next":
			if parsed.action != "" || parsed.account != "" || i+1 == len(argv) || !slices.Contains([]string{"mythos", "clear", "show"}, argv[i+1]) {
				return parsed, fmt.Errorf("--next requires mythos, clear or show and cannot be combined with management options")
			}
			i++
			parsed.action, parsed.id = tok, argv[i]
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
		case "--list", "--started", "--check":
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
	return startCommandFor(prog, argv, streams, "")
}

func startCommandFor(prog string, argv []string, streams ioStreams, selectedDir string) int {
	parsed, err := parseStartArguments(argv)
	if err != nil {
		return subError(prog+" start", streams.err, err.Error())
	}
	if parsed.help {
		return renderStartHelp(prog, streams.out)
	}
	if parsed.mythos && (parsed.action != "" || len(parsed.command) > 0) {
		return subError(prog+" start", streams.err, "--mythos takes only an optional --account ID")
	}
	if parsed.action == "--next" && parsed.id == "show" {
		return showNextStart(streams)
	}
	env := os.Environ()
	// The child gets env; this process acts on the default login under its locks.
	dropFableStoreEnv()
	if parsed.action == "--next" {
		model := parsed.id
		if model == "clear" {
			model = ""
		}
		if err := store.SetNextStartModel(paths.GetBackupRoot(), model); err != nil {
			startNotice(streams.err, err.Error())
			return 1
		}
		return 0
	}
	if parsed.action == "--started" {
		startStarted(streams.in)
		return 0
	}
	if parsed.action == "--check" {
		startCheck(streams.in, streams.out)
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
	return runStart(bin, parsed, selectedDir, env, streams, true)
}

func runStart(bin string, parsed startArguments, selectedDir string, env []string, streams ioStreams, checkNext bool) int {
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
	env = withStartVar(env, recoveredFableEnv, "")
	childEnv, hold, family := env, "", ""
	var link string
	native := launch.IsClaudeCommand(bin)
	override := startOverride(env, parsed.command, storeDir)
	var next store.NextStartRequest
	if checkNext && native && store.SupportsMythosStart() && !parsed.mythos && parsed.account == "" && len(args) == 1 && override == "" && startStoreProblem(root) == "" {
		var err error
		next, err = store.ReadNextStart(root)
		if err != nil {
			warnNextStart(streams.err, err)
		}
	}
	refuseStart := func(reason string) int {
		if next.Model == mythosFamily {
			reason = "Next start is set to Mythos: " + strings.TrimSuffix(reason, ".") + ". Use tycswap start --next clear to start without it."
		}
		startNotice(streams.err, reason)
		return 1
	}
	strictMythos := parsed.mythos || next.Model == mythosFamily
	if strictMythos {
		if !native || !store.SupportsMythosStart() {
			startNotice(streams.err, "Independent Mythos logins require native Claude on Linux, WSL, or Windows.")
			return 1
		}
		if reason := independentAuthOverride(env, root); reason != "" {
			return refuseStart("Cannot start an independent Mythos session because " + reason + ".")
		}
		args = append(slices.Clone(args), "--model", nativeMythosModel)
	}
	if reason := override; reason != "" {
		if native && sessionLinkPath != "" {
			// An unmanaged child can inherit the login but must not own the parent's link.
			dir := mythosSessionDir
			if fableSessionProcess {
				dir = storeDir
			}
			childEnv = withStartVar(env, secureStorageEnv, dir)
			if sessionLinkErr != nil {
				startNotice(streams.err, "Could not read the inherited session link: "+sessionLinkErr.Error())
			}
		}
		if parsed.account != "" {
			startNotice(streams.err, "Skipping the requested start login because "+reason+"; launching unchanged.")
		}
	} else {
		// Route a nested launch from scratch, including when its parent's link
		// has already disappeared. Never hand that link to a second session.
		if sessionLinkPath != "" {
			env = withStartVar(env, secureStorageEnv, "")
		}
		requestedFamily := startSessionFamily(args, env)
		model := requestedFamily
		var dir string
		linked := native && runtime.GOOS == "linux" && hasSessionLinkAccounts(root)
		var savedTarget string
		var err error
		linkErr := sessionLinkErr
		if id := resumedSession(args[1:]); linked && sessionUUID.MatchString(id) {
			saved, ok, readErr := reporting.LookupSessionLink(root, id)
			if readErr != nil {
				linkErr = readErr
			} else if ok {
				savedTarget = saved.Target
			}
		}
		if (model == mythosFamily || native) && (savedTarget == "" || isMythosDirectory(root, savedTarget)) {
			selectStart := func(useSelection bool, approved mythosPlacement) (mythosPlacement, error) {
				placement, err := selectMythosStart(root, model, parsed.account, args[1:], streams, useSelection, approved)
				if err == nil && strictMythos && (placement.dir == "" || selectedDir != "" && selectedDir != placement.dir) {
					err = fmt.Errorf("the independent Mythos login is unavailable or has changed")
				}
				return placement, err
			}
			var placement mythosPlacement
			placement, err = selectStart(linked && linkErr == nil, mythosPlacement{})
			if err == nil && placement.dir != "" && linked && linkErr == nil {
				link, linkErr = reporting.CreateSessionLink(root, placement.dir)
				if linkErr != nil && savedTarget == "" && parsed.account == "" {
					// A direct launch uses the original ranking. Keep consent only for the exact proposal.
					placement, err = selectStart(false, placement)
				}
			}
			dir = placement.dir
		} else if !native {
			model = ""
		}
		if err != nil {
			return refuseStart(err.Error())
		}
		var recovered string
		if dir == "" && (native || requestedFamily == "fable") && (savedTarget == "" || savedTarget == storeDir) {
			recovered, err = recoveredFableStart(root, requestedFamily, parsed.account, args[1:], native, savedTarget == storeDir)
			if err != nil {
				startNotice(streams.err, err.Error())
				return 1
			}
		}
		if recovered != "" {
			childEnv = withStartVar(withFableStore(env, storeDir), recoveredFableEnv, recovered)
			family = "fable"
		} else if dir != "" {
			family = model
			childEnv = withFableStore(env, dir)
		} else {
			env = withoutFableStore(env, mythosSessionDir)
			var code int
			childEnv, hold, family, code = startSharedLogin(env, args, root, storeDir, parsed.account, model, savedTarget, native, streams)
			if code != 0 {
				return code
			}
		}
		if linked {
			target := envValue(childEnv, secureStorageEnv)
			if target == "" {
				target = paths.GetClaudeConfigHome()
			}
			if link == "" && linkErr == nil {
				link, linkErr = reporting.CreateSessionLink(root, target)
			}
			if linkErr != nil {
				startNotice(streams.err, "Session link unavailable; launching on the selected login: "+linkErr.Error())
			} else {
				childEnv = withFableStore(childEnv, link)
				family = requestedFamily
			}
		}
	}
	childEnv = withStartVar(withStartHold(childEnv, hold), startFamilyEnv, family)
	if next.Model != "" {
		claimed, err := next.Claim(root)
		if !claimed {
			if link != "" {
				_ = os.Remove(link)
			}
			if err != nil {
				if !errors.Is(err, store.ErrUnreadableNextStart) {
					return refuseStart(err.Error())
				}
				warnNextStart(streams.err, err)
			}
			// The observed request is no longer available. Start without it.
			return runStart(bin, parsed, selectedDir, env, streams, false)
		}
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	release := claimSigint()
	defer release()
	code, err := startExec(bin, args, childEnv)
	if err != nil {
		if link != "" {
			_ = os.Remove(link)
		}
		if next.Model != "" {
			if restoreErr := next.Restore(root); restoreErr != nil {
				startNotice(streams.err, "Could not restore the next start: "+restoreErr.Error())
			}
		}
		startNotice(streams.err, "Could not launch command: "+err.Error())
	}
	return code
}

// An unmanaged start goes through the ordinary direct launcher, which also
// owns the diagnostics for an unreadable roster.
func hasSessionLinkAccounts(root string) bool {
	if startStoreProblem(root) != "" {
		return false
	}
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return false
	}
	data, err := sw.SequenceForUpdate()
	return err == nil && len(data.Accounts) != 0
}

func startSharedLogin(env, args []string, root, storeDir, account, model, savedTarget string, native bool, streams ioStreams) (childEnv []string, hold, family string, code int) {
	fable, hasFableLogins := startFableStore(root)
	var onFable bool
	var notice string
	if savedTarget == "" {
		childEnv, onFable, notice = startEnvironment(env, args, fable, hasFableLogins, storeDir)
	} else {
		childEnv, onFable, notice = startStoreEnvironment(env, fable, savedTarget == storeDir, storeDir)
	}
	if notice != "" {
		startNotice(streams.err, notice)
	}
	if onFable {
		var toDefault bool
		hold, toDefault = startSelectFable(account, streams.err, "", native)
		if toDefault {
			childEnv = withoutFableStore(env, storeDir)
			onFable = false
		}
	}
	if !onFable {
		if model == mythosFamily {
			family = mythosFamily
			hold = startSelectMythos(account, streams.err)
		} else {
			hold = startSelect(account, model, streams.err)
		}
	}
	if err := confirmSharedStart(root, onFable, model, args[1:], streams); err != nil {
		startNotice(streams.err, err.Error())
		if hold != "" {
			sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
			if err == nil {
				releaseStartHolds(sw, hold)
			}
		}
		return nil, "", "", 1
	}
	return childEnv, hold, family, 0
}

func startOverride(env, command []string, storeDir string) string {
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if envKeyIs(key, "CLAUDE_CONFIG_DIR") {
			return key + " is set"
		}
		if envKeyIs(key, secureStorageEnv) && (namesFableStore(value, storeDir, startWindows()) || isMythosDirectory(paths.GetBackupRoot(), value) || reporting.IsSessionLink(paths.GetBackupRoot(), value)) {
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
// one. With none of them in the pool, or without account by the Fable
// placement rule, toDefault is true: the session starts on the default login.
// Without marks, or with auto-switch off and no account, it changes nothing
// (DESIGN A105, A112). Its last notice names the accounts that need a Fable
// sign-in, unless the rule's notice ended in A112's hint (A115).
func selectFableStartLogin(account string, stderr io.Writer, token string, native bool) (hold string, toDefault bool) {
	sw, err := newSwitcher(store.Options{OAuth: oauth.NewHTTPClient(), Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return "", false
	}
	hinted := false
	defer func() {
		if !hinted {
			noteFableSignIn(sw.Store, stderr)
		}
	}()
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
	snapshot := func() (autoswitch.StartSnapshot, error) { return readFableStartSnapshot(sw, account, native) }
	snap, toDefault, err := fableStartsOnDefault(sw, account)
	if err != nil {
		reportStartSelection(sw, stderr, "Fable store: Could not select a start login: "+err.Error()+"; continuing with the current login.")
		return "", false
	}
	if toDefault {
		reportStartSelection(sw, stderr, noFableStartLogin+"; starting on the default login.")
		return "", true
	}
	if account == "" {
		if toDefault, notice, roomHint := fableSessionOnDefault(sw.Store); toDefault {
			reportStartSelection(sw, stderr, notice)
			hinted = roomHint
			return "", true
		}
	}
	if account == "" && (len(snap.Marked) == 0 || !snap.Settings.Enabled) {
		return "", false
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

// noFableStartLogin is why start sends a Fable session to the default login
// although the Fable store is usable (DESIGN A105, A108).
const noFableStartLogin = "no start login has a Fable login"

// fableStartsOnDefault is start's snapshot of the Fable store and whether
// start sends a Fable session to the default login on it: marks are set and
// auto-switch is on, or account is named, and none of them is in the Fable
// pool. start --check asks the same (DESIGN A108).
func fableStartsOnDefault(sw *core.Switcher, account string) (autoswitch.StartSnapshot, bool, error) {
	snap, err := readFableStartSnapshot(sw, account, false)
	if err != nil {
		return snap, false, err
	}
	return snap, (account != "" || len(snap.Marked) > 0 && snap.Settings.Enabled) && len(snap.Candidates) == 0, nil
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
	snap.CurrentDisabled = store.DisabledFrom(data, snap.Current)
	snap.CurrentMarked = !snap.CurrentDisabled && marked(data, snap.Current)
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
	if family == "opus" && account == "" && !snap.CurrentMarked && set.Enabled {
		pids, err := store.LiveProfilePIDs(view.DefaultProfileDir())
		snap.IdleOpus = err == nil && !slices.ContainsFunc(pids, func(pid int) bool { return policy.IndependentSessions[pid] == "" })
		snap.ReservedLast = policy.ReservedLast
	}
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if snap.IdleOpus {
			if models, allocated := store.ModelsFrom(data, num); allocated && !slices.Contains(models, "opus") {
				continue
			}
		}
		if (account == "" && marked(data, num) && serves(num) || account == num) && view.RotationEligible(data, num) {
			snap.Candidates = append(snap.Candidates, num)
		}
	}
	if family == "fable" && account == "" {
		preferFableAllocations(&snap, view, data, true)
	}
	snap.Entries = (&core.Switcher{Store: view}).UsageEntriesByAccount(map[string]bool{})
	return snap, nil
}

// observeFableStartSnapshot is a test seam, nil in production: it is told of
// every Fable start snapshot read.
var observeFableStartSnapshot func()

// readFableStartSnapshot is start's snapshot of the Fable store: the live
// slot from backup or chain only, the candidates the marked accounts (or
// account) in the Fable pool, counted on the Fable windows as the Fable tick
// counts them. It makes no request.
func readFableStartSnapshot(sw *core.Switcher, account string, native bool) (autoswitch.StartSnapshot, error) {
	if observeFableStartSnapshot != nil {
		observeFableStartSnapshot()
	}
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
	snap.CurrentDisabled = store.DisabledFrom(data, snap.Current)
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
	if native && account == "" {
		preferFableAllocations(&snap, view, data, false)
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

func showNextStart(streams ioStreams) int {
	model := "none"
	if store.SupportsMythosStart() {
		request, err := store.ReadNextStart(paths.GetBackupRoot())
		if err != nil {
			warnNextStart(streams.err, err)
			return 1
		}
		if request.Model != "" {
			model = request.Model
		}
	}
	fmt.Fprintln(streams.out, model)
	return 0
}

func warnNextStart(stderr io.Writer, err error) {
	startNotice(stderr, fmt.Sprintf("Ignoring %v. Use tycswap start --next clear to clear the request.", err))
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
	fmt.Fprintf(out, "       %s start --mythos [--account ID]\n", prog)
	fmt.Fprintf(out, "       %s start --next mythos|clear\n", prog)
	fmt.Fprintf(out, "       %s start --next show\n", prog)
	fmt.Fprintf(out, "       %s start --allow ID | --disallow ID | --list\n", prog)
	fmt.Fprintf(out, "       %s start --started\n", prog)
	fmt.Fprintf(out, "       %s start --check\n", prog)
	fmt.Fprintln(out, "\nStart on a marked login, then launch once (default: real Claude). A marked login with room")
	fmt.Fprintln(out, "comes first; one at its limit is still used, and auto-switch leaves it there until the session")
	fmt.Fprintln(out, "is up: --started, run as Claude Code's SessionStart hook, says so, else the hold ends after 30 s.")
	fmt.Fprintln(out, "--next show prints mythos or none and leaves the request in place.")
	fmt.Fprintln(out, "Selection failures launch on the current login. Arguments pass unchanged.")
	fmt.Fprintln(out, "--account also works with auto-switch off; inherited auth/profile overrides skip selection.")
	fmt.Fprintln(out, "Sessions on a Fable model start on the Fable store when one is on (tycswap add --login --lineage fable),")
	fmt.Fprintln(out, "which moves to a marked login with a Fable login too; with marks but none such, they start on the default login.")
	fmt.Fprintln(out, "While no Fable login has room, they start on the default login when its account has Fable room.")
	fmt.Fprintln(out, "With the Fable store on and usable, they also name the accounts that need a Fable sign-in (tycswap fable --login).")
	fmt.Fprintln(out, "Sessions on Mythos start on an account marked with tycswap mythos --allow ID, while one is marked.")
	fmt.Fprintln(out, "For native Claude on Linux and WSL, CLAUDE_SECURESTORAGE_CONFIG_DIR points to a per-session link.")
	fmt.Fprintln(out, "--started binds that link. The status line moves it when Claude changes model. To prepare")
	fmt.Fprintln(out, "for Mythos, run ! tycswap use mythos in the session, then /model claude-mythos-5-2.")
	fmt.Fprintln(out, "In-process subagents share this login. If Remote Control disconnects after a move, reconnect with /remote-control.")
	fmt.Fprintln(out, "macOS, Windows and existing unlinked sessions keep their original login. --check reports a mismatch.")
	fmt.Fprintln(out, "TYCSWAP_START_HOLD records a start hold until the SessionStart hook releases it.")
	return 0
}
