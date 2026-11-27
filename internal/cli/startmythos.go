package cli

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

func isMythosDirectory(root, dir string) bool {
	if dir == "" || !namesFableStore(filepath.Dir(dir), store.MythosLoginRoot(root), startWindows()) {
		return false
	}
	return validMythosDigest(filepath.Base(dir))
}

func validMythosDigest(digest string) bool {
	_, err := hex.DecodeString(digest)
	return len(digest) == 64 && err == nil
}

var errNoMythosQuota = errors.New("no available Mythos login has quota")
var errNoMythosLogin = errors.New("no enabled, marked account with an independent Mythos login is available")

type mythosPlacement struct {
	dir, number, name, allocation string
	// notice is start's line for a start on a start login, seed the login it
	// records as the selection when there is none (DESIGN A120).
	notice, seed string
	// spent: a session leaves the login it came up on for this one although
	// it is at its limit (DESIGN A125).
	spent bool
}

var askPlacement = func(streams ioStreams, question, detail string, printMode bool) (bool, error) {
	in, inputOK := streams.in.(*os.File)
	out, outputOK := streams.err.(*os.File)
	if printMode || !inputOK || !outputOK || !humanTerminal(isTTY(in), isTTY(out)) {
		if broker := os.Getenv("TYCSWAP_PLACEMENT_BROKER"); broker != "" {
			fmt.Fprintln(streams.err, "Operator decision needed:", question)
			return askPlacementBroker(broker, question, detail)
		}
		return false, fmt.Errorf("operator decision needed: %s Run in a terminal or configure TYCSWAP_PLACEMENT_BROKER", question)
	}
	i, _, ok := askMenu(streams, question, []string{"Wait", placementUse}, "")
	return ok && i == 1, nil
}

func selectMythosStart(root, model, account string, args []string, streams ioStreams, linked bool, approved mythosPlacement) (mythosPlacement, error) {
	session := resumedSession(args)
	locate := func() (mythosPlacement, error) {
		if linked {
			return locateMythosLogin(root, model, account, session)
		}
		return locateStartMythos(root, model, account, session, false)
	}
	proposed, err := locate()
	if err != nil || proposed.allocation == "" || proposed == approved {
		return proposed, err
	}
	if err := confirmStartAllocation(proposed.name, proposed.allocation, model, args, streams); err != nil {
		return mythosPlacement{}, err
	}
	current, err := locate()
	if err != nil {
		return mythosPlacement{}, err
	}
	if current != proposed {
		return mythosPlacement{}, fmt.Errorf("account or allocation changed while awaiting the answer; retry the start")
	}
	return proposed, nil
}

func locateMythosLogin(root, model, account, session string) (mythosPlacement, error) {
	return locateStartMythos(root, model, account, session, runtime.GOOS == "linux")
}

// locateStartMythos is a Mythos start's login. A linked start, fresh or a
// resume, begins on a start login (DESIGN A120).
func locateStartMythos(root, model, account, session string, linked bool) (mythosPlacement, error) {
	var scope string
	if sessionUUID.MatchString(session) {
		var err error
		scope, err = reporting.SessionBinding(root, session)
		if err != nil {
			return mythosPlacement{}, err
		}
	}
	if strings.HasPrefix(scope, "fable/") {
		return mythosPlacement{}, nil
	}
	bound, dedicated := strings.CutPrefix(scope, "mythos/")
	if scope != "" && !dedicated {
		return mythosPlacement{}, fmt.Errorf("the session's recorded login identity is invalid")
	}
	if !dedicated && model != mythosFamily {
		return mythosPlacement{}, nil
	}
	if dedicated && !validMythosDigest(bound) {
		return mythosPlacement{}, fmt.Errorf("the session's recorded login identity is invalid")
	}
	if _, err := os.Stat(store.MythosLoginRoot(root)); os.IsNotExist(err) {
		if dedicated {
			return mythosPlacement{}, fmt.Errorf("the session's recorded login is unavailable")
		}
		return mythosPlacement{}, nil
	} else if err != nil {
		return mythosPlacement{}, err
	}
	if problem := startStoreProblem(root); problem != "" {
		return mythosPlacement{}, fmt.Errorf("could not verify the Mythos store at %s", root)
	}
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return mythosPlacement{}, err
	}
	view := sw.Store.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	set, err := loadStartSettings(root)
	if err != nil {
		return mythosPlacement{}, err
	}
	selected := ""
	if linked && account == "" {
		// The link creator reports a damaged selection and falls back to this direct placement.
		selected, _ = store.SelectedMythosDigest(root)
	}
	return chooseMythosLogin(view, model, mythosLoginRequest{account: account, bound: bound, selected: selected, start: true, startLogins: linked}, set)
}

func mythosMovePlacement(view *store.Store) (mythosPlacement, error) {
	set, err := loadStartSettings(view.BackupDir())
	if err != nil {
		return mythosPlacement{}, err
	}
	return mythosTickPlacement(view, "", set, false)
}

func mythosHandSelection(view *store.Store, set settings.AutoSwitchSettings, readOnly bool) (string, string) {
	path := mythosStatePath(view.BackupDir())
	var warn func(error)
	if !readOnly {
		warn = func(err error) {
			logging.NewWithClock(view.BackupDir(), false, view.Clk).Warningf("Ignoring unreadable Mythos switch cooldown at %s: %v", path, err)
		}
	}
	return autoswitch.HandSelection(path, view.Clk, set, warn)
}

// handMythosChoice resolves a manual cooldown against the current selection.
func handMythosChoice(view *store.Store, selected string, set settings.AutoSwitchSettings, dryRun bool) (mythosPlacement, string, error) {
	var none mythosPlacement
	if selected == "" {
		return none, "", nil
	}
	num, hold := mythosHandSelection(view, set, dryRun)
	if num == "" {
		return none, "", nil
	}
	data, err := view.SequenceForUpdate()
	if err != nil {
		return none, "", err
	}
	login, err := view.MythosLogin(data, num)
	if err != nil || !login.Ready || filepath.Base(login.Dir) != selected {
		return none, "", err
	}
	return mythosPlacement{dir: login.Dir, number: num, name: store.AccountNameFrom(data, num)}, hold, nil
}

func mythosTickPlacement(view *store.Store, sourceDir string, set settings.AutoSwitchSettings, dryRun bool) (mythosPlacement, error) {
	selected, err := store.SelectedMythosDigest(view.BackupDir())
	if err != nil {
		return mythosPlacement{}, err
	}
	if sourceDir != "" {
		choice, hold, err := handMythosChoice(view, selected, set, dryRun)
		if err != nil {
			return mythosPlacement{}, err
		}
		if !set.Enabled || hold != "" {
			if choice.dir != sourceDir {
				return choice, nil
			}
			return mythosPlacement{}, nil
		}
	}
	return chooseMythosLogin(view, mythosFamily, mythosLoginRequest{source: sourceDir, selected: selected}, set)
}

type mythosLoginRequest struct {
	account, bound, source, selected string
	start                            bool
	// startLogins: a linked start, which begins on a start login (DESIGN A120).
	startLogins bool
}

func chooseMythosLogin(view *store.Store, model string, req mythosLoginRequest, set settings.AutoSwitchSettings) (mythosPlacement, error) {
	data, err := view.SequenceForUpdate()
	if err != nil {
		return mythosPlacement{}, err
	}
	if req.account != "" {
		req.account, _, _, err = view.ResolveAccountFrom(data, req.account)
		if err != nil {
			return mythosPlacement{}, err
		}
	}
	var sourceNumber string
	// logins are the ready logins; candidates those the request allows.
	var logins, candidates []string
	placements := map[string]mythosPlacement{}
	ids := map[string]usage.Identity{}
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		dir, ready := view.MythosLoginReady(data, num)
		allowed := (req.account == "" || req.account == num) && (req.bound == "" || filepath.Base(dir) == req.bound)
		if !ready || !allowed && !req.startLogins {
			continue
		}
		logins = append(logins, num)
		if allowed {
			candidates = append(candidates, num)
			if dir == req.source {
				sourceNumber = num
			}
		}
		placement := mythosPlacement{dir: dir, number: num, name: store.AccountNameFrom(data, num)}
		if models, explicit := store.ModelsFrom(data, num); explicit && !slices.Contains(models, model) {
			placement.allocation = strings.Join(models, ", ")
		}
		placements[num] = placement
		_, email, org, _ := view.ResolveAccountFrom(data, num)
		ids[num] = usage.Identity{Email: email, OrgUUID: org}
	}
	if len(candidates) == 0 {
		if req.bound != "" {
			return mythosPlacement{}, fmt.Errorf("the session's recorded login is unavailable for this resume")
		}
		return mythosPlacement{}, fmt.Errorf("%w for this start", errNoMythosLogin)
	}
	entries := view.Usage.Entries(ids)
	for num, entry := range entries {
		entry.AuthDeadStrikes = 0 // credential failures belong to the default grant, independently of shared quota
		entries[num] = entry
	}
	now := view.Clk.Now()
	rank := autoswitch.InitialPlacementTargets
	if req.source != "" {
		rank = autoswitch.KnownRoomTargets
		// Finish an earlier rotation even if the old login's quota has since reset.
		if req.selected != "" && filepath.Base(req.source) != req.selected {
			for _, n := range rank(candidates, entries, set, model, now) {
				if filepath.Base(placements[n].dir) == req.selected {
					return placements[n], nil
				}
			}
		}
		if limits, _ := modelLimitWindows(entries[sourceNumber], model, now); sourceNumber != "" && len(limits) == 0 {
			return mythosPlacement{}, nil
		}
	}
	ranked := rank(candidates, entries, set, model, now)
	var choice mythosPlacement
	if len(ranked) > 0 {
		choice = rankedMythosChoice(view, req, ranked, placements, set)
	}
	if req.startLogins {
		room, would := ranked, choice.number
		if req.account != "" || req.bound != "" {
			room = selectedFirst(autoswitch.InitialPlacementTargets(logins, entries, set, model, now), placements, req.selected)
		}
		if req.bound != "" {
			would = candidates[0]
		}
		if login, ok := startLoginInstead(data, req, logins, ranked, room, would, placements, set); ok {
			return login, nil
		}
	}
	if len(ranked) == 0 {
		err := fmt.Errorf("%w for %s", errNoMythosQuota, model)
		if req.start {
			if number, reset := firstMythosReturn(candidates, entries, model, now); number != "" {
				err = fmt.Errorf("%w; Account-%s returns %s", err, number, oauth.ResetClockString(reset, now))
			}
		}
		return mythosPlacement{}, err
	}
	return choice, nil
}

// rankedMythosChoice is the login with room the ranking chooses: the selected
// one first, then the best ranked without an allocation question.
func rankedMythosChoice(view *store.Store, req mythosLoginRequest, ranked []string, placements map[string]mythosPlacement, set settings.AutoSwitchSettings) mythosPlacement {
	selectedFirst(ranked, placements, req.selected)
	// A hand-selected account with room is the start target, including when
	// the existing allocation question must be answered before launch.
	if req.selected != "" && req.source == "" && filepath.Base(placements[ranked[0]].dir) == req.selected && placements[ranked[0]].allocation != "" {
		hand, _ := mythosHandSelection(view, set, true)
		if hand == ranked[0] {
			return placements[hand]
		}
	}
	for _, num := range ranked {
		if placements[num].allocation == "" {
			return placements[num]
		}
	}
	return placements[ranked[0]]
}

// selectedFirst moves the selected login to the front of ranked, in place.
func selectedFirst(ranked []string, placements map[string]mythosPlacement, selected string) []string {
	if selected != "" {
		if preferred := slices.IndexFunc(ranked, func(num string) bool { return filepath.Base(placements[num].dir) == selected }); preferred > 0 {
			num := ranked[preferred]
			copy(ranked[1:], ranked[:preferred])
			ranked[0] = num
		}
	}
	return ranked
}

// startLoginInstead is the start login a linked Mythos start takes instead of
// would, the login it would get ("" for none): the ranked choice with room,
// for a resume the recorded login. It returns the start's notice; false keeps
// the start as it was (DESIGN A120). A start login is a ready Mythos login
// marked as a start login and not allocated away from Mythos. ranked is the
// requested logins with room, room all ready logins with room, both ranked.
//
// Without an account, when would is no start login, the start takes the best
// ranked start login with room, else one at its limit while another login has
// room: the selected Mythos login if it is one, else the first in sequence
// order. That needs auto-switch on, except for a resume whose recorded login
// has room: the session returns there by itself. With an account, only that
// account, when it is a start login at its limit and another login has room;
// also with auto-switch off, as A105 takes an explicit start login at its
// limit. A start login at its limit needs a login with room that asks no
// allocation question, which a move would wait for. The start seeds the
// selection with would, not the start login.
func startLoginInstead(data *store.SequenceData, req mythosLoginRequest, logins, ranked, room []string, would string, placements map[string]mythosPlacement, set settings.AutoSwitchSettings) (mythosPlacement, bool) {
	isStart := func(num string) bool { return store.StartLoginFrom(data, num) && placements[num].allocation == "" }
	free := slices.ContainsFunc(room, func(num string) bool { return placements[num].allocation == "" })
	atLimit := func(num string) mythosPlacement {
		login := placements[num]
		login.notice = "Starting on start login Account-" + num + ", which is at its limit"
		if set.Enabled {
			login.notice += "; auto-switch moves the session to a Mythos login with room once it is up"
		}
		login.notice += "."
		return login
	}
	if req.account != "" {
		if !isStart(req.account) || len(ranked) > 0 || !free {
			return mythosPlacement{}, false
		}
		return atLimit(req.account), true
	}
	returns := req.bound != "" && len(ranked) > 0
	if would == "" || store.StartLoginFrom(data, would) || !set.Enabled && !returns {
		return mythosPlacement{}, false
	}
	for _, num := range room {
		if isStart(num) {
			login := placements[num]
			login.notice, login.seed = "Starting on start login Account-"+num+".", placements[would].dir
			return login, true
		}
	}
	var spent []string
	for _, num := range logins {
		if isStart(num) && !slices.Contains(room, num) {
			spent = append(spent, num)
		}
	}
	if len(spent) == 0 || !free {
		return mythosPlacement{}, false
	}
	i := slices.IndexFunc(spent, func(num string) bool { return filepath.Base(placements[num].dir) == req.selected })
	if req.selected == "" || i < 0 {
		i = 0
	}
	login := atLimit(spent[i])
	login.seed = placements[would].dir
	return login, true
}

func firstMythosReturn(candidates []string, entries map[string]usage.UsageEntry, model string, now time.Time) (string, time.Time) {
	var number string
	var earliest time.Time
	for _, num := range candidates {
		_, reset := modelLimitWindows(entries[num], model, now)
		if !reset.IsZero() && (earliest.IsZero() || reset.Before(earliest)) {
			number, earliest = num, reset
		}
	}
	return number, earliest
}

const nativeMythosModel = "claude-mythos-5-2"

func independentAuthOverride(env []string, root string) string {
	if reason := startOverride(env, nil, paths.FableStoreDir(root)); reason != "" {
		return reason
	}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if value != "" && independentProviderOverride(key) {
			return key + " is set"
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "the current directory is unavailable"
	}
	for _, path := range []string{paths.GetClaudeSettingsPath(), filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json")} {
		settings, err := ccfile.ReadGlobalConfigStrict(path)
		if err != nil {
			return "the Claude settings at " + path + " could not be verified"
		}
		if helper, set := settings["apiKeyHelper"]; set && helper != "" {
			return path + " sets apiKeyHelper"
		}
		value, set := settings["env"]
		vars, ok := value.(map[string]any)
		if set && !ok {
			return "the environment in " + path + " could not be verified"
		}
		for key, value := range vars {
			if value != "" && (independentProviderOverride(key) || identitySetting(key)) {
				return path + " sets env." + key
			}
		}
	}
	return ""
}

func identitySetting(key string) bool {
	for _, override := range launch.AuthOverrideEnvVars {
		if envKeyIs(key, override) {
			return true
		}
	}
	return envKeyIs(key, "CLAUDE_CONFIG_DIR")
}

func independentProviderOverride(key string) bool {
	if startWindows() {
		key = strings.ToUpper(key)
	}
	switch key {
	case "ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_FOUNDRY":
		return true
	}
	return strings.HasPrefix(key, "ANTHROPIC_") && strings.HasSuffix(key, "_BASE_URL")
}
