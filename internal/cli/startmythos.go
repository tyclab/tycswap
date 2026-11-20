package cli

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/reporting"
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

type mythosPlacement struct {
	dir, number, allocation string
}

var askPlacement = func(streams ioStreams, question string, printMode bool) (bool, error) {
	in, inputOK := streams.in.(*os.File)
	out, outputOK := streams.err.(*os.File)
	if printMode || !inputOK || !outputOK || !humanTerminal(isTTY(in), isTTY(out)) {
		if broker := os.Getenv("TYCSWAP_PLACEMENT_BROKER"); broker != "" {
			fmt.Fprintln(streams.err, "Operator decision needed:", question)
			return askPlacementBroker(broker, question)
		}
		return false, fmt.Errorf("operator decision needed: %s Run in a terminal or configure TYCSWAP_PLACEMENT_BROKER", question)
	}
	i, _, ok := askMenu(streams, question, []string{"Wait", placementUse}, "")
	return ok && i == 1, nil
}

func mythosStartDir(root, model, account string, args []string, streams ioStreams) (string, error) {
	session := resumedSession(args)
	proposed, err := locateMythosLogin(root, model, account, session)
	if err != nil || proposed.allocation == "" {
		return proposed.dir, err
	}
	if err := confirmStartAllocation(proposed.number, proposed.allocation, model, args, streams); err != nil {
		return "", err
	}
	current, err := locateMythosLogin(root, model, account, session)
	if err != nil {
		return "", err
	}
	if current != proposed {
		return "", fmt.Errorf("account or allocation changed while awaiting the answer; retry the start")
	}
	return current.dir, nil
}

func locateMythosLogin(root, model, account, session string) (mythosPlacement, error) {
	var scope string
	if sessionUUID.MatchString(session) {
		_, scope, _ = reporting.RecordedSession(root, session)
	}
	bound, dedicated := strings.CutPrefix(scope, "mythos/")
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
	var candidates []string
	placements := map[string]mythosPlacement{}
	ids := map[string]usage.Identity{}
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		dir, ready := view.MythosLoginReady(data, num)
		if ready && (account == "" || account == num) && (!dedicated || filepath.Base(dir) == bound) {
			candidates = append(candidates, num)
			placement := mythosPlacement{dir: dir, number: num}
			if models, explicit := store.ModelsFrom(data, num); explicit && !slices.Contains(models, model) {
				placement.allocation = strings.Join(models, ", ")
			}
			placements[num] = placement
			_, email, org, _ := view.ResolveAccountFrom(data, num)
			ids[num] = usage.Identity{Email: email, OrgUUID: org}
		}
	}
	if len(candidates) == 0 {
		if dedicated {
			return mythosPlacement{}, fmt.Errorf("the session's recorded login is unavailable for this resume")
		}
		return mythosPlacement{}, fmt.Errorf("no enabled, marked account with an independent Mythos login is available for this start")
	}
	settings, err := loadStartSettings(root)
	if err != nil {
		return mythosPlacement{}, err
	}
	entries := view.Usage.Entries(ids)
	for num, entry := range entries {
		entry.AuthDeadStrikes = 0 // credential failures belong to the default grant, independently of shared quota
		entries[num] = entry
	}
	ranked := autoswitch.InitialPlacementTargets(candidates, entries, settings, model, view.Clk.Now())
	if len(ranked) == 0 {
		return mythosPlacement{}, fmt.Errorf("no available Mythos login has quota for this %s start", model)
	}
	for _, num := range ranked {
		if placements[num].allocation == "" {
			return placements[num], nil
		}
	}
	return placements[ranked[0]], nil
}
