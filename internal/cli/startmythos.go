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

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/keychain"
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
	if linked && account == "" && bound == "" {
		// The link creator reports a damaged selection and falls back to this direct placement.
		selected, _ = store.SelectedMythosDigest(root)
	}
	return chooseMythosLogin(view, model, account, bound, "", selected, set)
}

func mythosMovePlacement(view *store.Store) (mythosPlacement, error) {
	set, err := loadStartSettings(view.BackupDir())
	if err != nil {
		return mythosPlacement{}, err
	}
	return mythosTickPlacement(view, "", set)
}

func mythosTickPlacement(view *store.Store, sourceDir string, set settings.AutoSwitchSettings) (mythosPlacement, error) {
	selected, err := store.SelectedMythosDigest(view.BackupDir())
	if err != nil {
		return mythosPlacement{}, err
	}
	return chooseMythosLogin(view, mythosFamily, "", "", sourceDir, selected, set)
}

func chooseMythosLogin(view *store.Store, model, account, bound, source, selected string, set settings.AutoSwitchSettings) (mythosPlacement, error) {
	data, err := view.SequenceForUpdate()
	if err != nil {
		return mythosPlacement{}, err
	}
	if account != "" {
		account, _, _, err = view.ResolveAccountFrom(data, account)
		if err != nil {
			return mythosPlacement{}, err
		}
	}
	var sourceNumber string
	var candidates []string
	placements := map[string]mythosPlacement{}
	ids := map[string]usage.Identity{}
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		dir, ready := view.MythosLoginReady(data, num)
		if ready && (account == "" || account == num) && (bound == "" || filepath.Base(dir) == bound) {
			candidates = append(candidates, num)
			if dir == source {
				sourceNumber = num
			}
			placement := mythosPlacement{dir: dir, number: num, name: store.AccountNameFrom(data, num)}
			if models, explicit := store.ModelsFrom(data, num); explicit && !slices.Contains(models, model) {
				placement.allocation = strings.Join(models, ", ")
			}
			placements[num] = placement
			_, email, org, _ := view.ResolveAccountFrom(data, num)
			ids[num] = usage.Identity{Email: email, OrgUUID: org}
		}
	}
	if len(candidates) == 0 {
		if bound != "" {
			return mythosPlacement{}, fmt.Errorf("the session's recorded login is unavailable for this resume")
		}
		return mythosPlacement{}, fmt.Errorf("%w for this start", errNoMythosLogin)
	}
	entries := view.Usage.Entries(ids)
	for num, entry := range entries {
		entry.AuthDeadStrikes = 0 // credential failures belong to the default grant, independently of shared quota
		entries[num] = entry
	}
	rank := autoswitch.InitialPlacementTargets
	if source != "" {
		rank = autoswitch.KnownRoomTargets
		// Finish an earlier rotation even if the old login's quota has since reset.
		if selected != "" && filepath.Base(source) != selected {
			for _, n := range rank(candidates, entries, set, model, view.Clk.Now()) {
				if filepath.Base(placements[n].dir) == selected {
					return placements[n], nil
				}
			}
		}
		if remaining := startModelHeadroom(entries[sourceNumber], model, view.Clk.Now()); remaining == nil || *remaining > 0 {
			return mythosPlacement{}, nil
		}
	}
	ranked := rank(candidates, entries, set, model, view.Clk.Now())
	if len(ranked) == 0 {
		return mythosPlacement{}, fmt.Errorf("%w for %s", errNoMythosQuota, model)
	}
	if selected != "" {
		if preferred := slices.IndexFunc(ranked, func(num string) bool { return filepath.Base(placements[num].dir) == selected }); preferred > 0 {
			num := ranked[preferred]
			copy(ranked[1:], ranked[:preferred])
			ranked[0] = num
		}
	}
	for _, num := range ranked {
		if placements[num].allocation == "" {
			return placements[num], nil
		}
	}
	return placements[ranked[0]], nil
}
