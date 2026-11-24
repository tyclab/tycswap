package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
	"github.com/tyclab/tycswap/internal/usage"
)

func confirmStartAllocation(name, allocation, model string, args []string, streams ioStreams) error {
	start := fmt.Sprintf("start %d", os.Getpid())
	if newID, _ := flagValue(args, "--session-id"); sessionUUID.MatchString(newID) {
		start = newID
	} else if resumed := resumedSession(args); sessionUUID.MatchString(resumed) {
		start = resumed
	}
	where := ""
	if cwd, err := os.Getwd(); err == nil {
		where = " in " + termsafe.Strip(filepath.Base(cwd))
	}
	family := "Claude"
	if model != "" {
		family = strings.ToUpper(model[:1]) + model[1:]
	}
	question := fmt.Sprintf("Use %s’s shared quota for this %s session%s? It is allocated to %s.", termsafe.Strip(name), family, where, allocation)
	if stop := slices.Index(args, "--"); stop >= 0 {
		args = args[:stop]
	}
	ok, err := askPlacement(streams, question, start, slices.Contains(args, "-p") || slices.Contains(args, "--print"))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s start deferred. Start again when an eligible placement is available", family)
	}
	return nil
}

type sharedStartException struct {
	number, name, allocation string
	identity                 usage.Identity
}

// Rank an ordinary eligible choice ahead of an exception only when start is
// already selecting a new login and the native registry has no live process.
func preferFableAllocations(snap *autoswitch.StartSnapshot, view *store.Store, data *store.SequenceData, defaultSelection bool) {
	if !snap.Settings.Enabled || snap.CurrentMarked || len(snap.Candidates) < 2 {
		return
	}
	exceptions := map[string]bool{}
	for _, num := range snap.Candidates {
		if models, set := store.ModelsFrom(data, num); set && !slices.Contains(models, "fable") {
			exceptions[num] = true
		}
	}
	if len(exceptions) == 0 || len(exceptions) == len(snap.Candidates) {
		return
	}
	if pids, err := store.LiveProfilePIDs(view.DefaultProfileDir()); err != nil || len(pids) != 0 {
		return
	}
	if defaultSelection && view.FableStore().Usable {
		_, toDefault, err := fableStartsOnDefault(&core.Switcher{Store: view}, "")
		if err == nil && !toDefault {
			toDefault, _ = fableSessionOnDefault(view)
		}
		if err != nil || !toDefault {
			return
		}
	}
	snap.FableAllocationExceptions = exceptions
}

// Check the credential the new process will inherit, including when selection
// kept the active account. A one-time answer applies to that identity alone.
func confirmSharedStart(root string, fable bool, family string, args []string, streams ioStreams) error {
	proposed, err := readSharedStartException(root, fable, family)
	if err != nil || proposed.allocation == "" {
		return err
	}
	if err := confirmStartAllocation(proposed.name, proposed.allocation, family, args, streams); err != nil {
		return err
	}
	current, err := readSharedStartException(root, fable, family)
	if err != nil {
		return err
	}
	if current != proposed {
		return fmt.Errorf("account or allocation changed while awaiting the answer; retry the start")
	}
	return nil
}

func readSharedStartException(root string, fable bool, family string) (sharedStartException, error) {
	var none sharedStartException
	_, err := os.Stat(filepath.Join(root, "sequence.json"))
	if os.IsNotExist(err) {
		return none, nil
	}
	checkAllocation := family == "opus" || family == "fable" || family == mythosFamily
	if problem := startStoreProblem(root); problem != "" {
		if checkAllocation {
			return none, fmt.Errorf("could not verify this launch's allocation in the account store")
		}
		return none, nil
	}
	if err != nil {
		return none, err
	}
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return none, fmt.Errorf("could not verify the account for launch: %w", err)
	}
	view := sw.Store.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	data, err := view.SequenceForUpdate()
	if err != nil {
		return none, err
	}
	hasDisabled, hasExclusions := false, false
	for _, number := range data.Sequence {
		n := strconv.Itoa(number)
		hasDisabled = hasDisabled || store.DisabledFrom(data, n)
		if checkAllocation {
			models, allocated := store.ModelsFrom(data, n)
			hasExclusions = hasExclusions || allocated && !slices.Contains(models, family)
		}
	}
	if !hasDisabled && !hasExclusions {
		return none, nil
	}
	if fable {
		view, err = view.ForFable()
		if err != nil {
			return none, err
		}
	}
	live := view.ResolveOwnerSlot(data)
	if live.Slot == "" {
		return none, fmt.Errorf("could not verify the current login for this launch")
	}
	if store.DisabledFrom(data, live.Slot) {
		return none, fmt.Errorf("Account-%s is disabled. Select an enabled login before starting", live.Slot)
	}
	if !hasExclusions {
		return none, nil
	}
	if view.AccountKindFor(live.Slot) == "oauth" && (live.Source == store.SourceConfig || live.Source == store.SourceLastInstall) {
		return none, fmt.Errorf("could not verify the current login from its credential for this launch")
	}
	models, allocated := store.ModelsFrom(data, live.Slot)
	if !allocated || slices.Contains(models, family) {
		return none, nil
	}
	number, email, org, err := view.ResolveAccountFrom(data, live.Slot)
	if err != nil {
		return none, err
	}
	id := usage.Identity{Email: email, OrgUUID: org}
	entry := view.Usage.Entries(map[string]usage.Identity{number: id})[number]
	if limits, _ := modelLimitWindows(entry, family, view.Clk.Now()); len(limits) > 0 {
		return none, fmt.Errorf("Account-%s has no remaining quota for this %s start", number, family)
	}
	return sharedStartException{number: number, name: store.AccountNameFrom(data, number), allocation: strings.Join(models, ", "), identity: id}, nil
}

func modelLimitWindows(entry usage.UsageEntry, family string, now time.Time) ([]string, time.Time) {
	return reporting.ModelLimitWindows(entry, family, now)
}
