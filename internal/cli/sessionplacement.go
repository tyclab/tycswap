package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
	"github.com/tyclab/tycswap/internal/usage"
)

func useCommand(prog string, args []string, streams ioStreams) int {
	const syntax = "use <mythos|fable|default> [--session ID]"
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(streams.out, "usage:", prog, syntax)
		return 0
	}
	if len(args) != 1 && len(args) != 3 || !slices.Contains([]string{"mythos", "fable", "default"}, args[0]) {
		return subError(prog+" use", streams.err, "expected "+syntax)
	}
	id, path := "", sessionLinkPath
	if len(args) == 3 {
		if args[1] != "--session" || !sessionUUID.MatchString(args[2]) {
			return subError(prog+" use", streams.err, "expected --session followed by a session ID")
		}
		id, path = args[2], ""
	}
	if runtime.GOOS != "linux" || path == "" && id == "" {
		errorTo(streams.err, "Run tycswap use inside a linked Linux or WSL session, or pass --session ID.")
		return 1
	}
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	var result sessionMoveResult
	if err == nil {
		result, err = placeSession(sw, id, path, "", args[0], false, "use")
	}
	if err != nil {
		errorTo(streams.err, err.Error())
		return 1
	}
	if result.line != "" {
		fmt.Fprintln(streams.out, result.line)
	}
	return 0
}

func modelHint(wanted string) string {
	switch wanted {
	case "mythos":
		return "Mythos login ready: /model claude-mythos-5-2"
	case "fable":
		return "Fable login ready: /model fable"
	}
	return ""
}

type sessionMoveResult struct {
	line     string
	news     bool
	question *sessionQuestion
}

type sessionQuestion struct {
	id, path, model, placementModel, claim, text, actor string
}

func placeSession(sw *core.Switcher, id, path, reportedModel, requested string, observe bool, actor string) (sessionMoveResult, error) {
	result, err := evaluateSessionPlacement(sw, id, path, reportedModel, requested, observe, "", actor)
	if err == nil && result.question != nil {
		err = startSessionAsker(*result.question)
	}
	return result, err
}

func evaluateSessionPlacement(sw *core.Switcher, id, path, reportedModel, requested string, observe bool, expected, actor string) (sessionMoveResult, error) {
	var result sessionMoveResult
	family := reporting.ClaudeFamily(reportedModel)
	if requested == "" && (strings.TrimSpace(reportedModel) == "" || !sessionUUID.MatchString(id)) {
		return result, nil
	}
	view := sw.Store.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	var repair []reporting.SessionLinkRecovery
	if expected == "" {
		source := reporting.SessionLinkRecovery{ObservedFamily: family}
		if path != "" && path == sessionLinkPath {
			source.LaunchFamily = os.Getenv(startFamilyEnv)
			if digest := os.Getenv(recoveredFableEnv); validMythosDigest(digest) {
				source.FableBinding = "fable/" + digest
			}
		}
		repair = append(repair, source)
	}
	err := reporting.MoveSessionLink(sw.Store, actor, id, path, requested != "", func(rec *reporting.SessionLink) error {
		// Explicit use can retry after a client refresh. Each attempt produces its own result.
		result = sessionMoveResult{}
		if expected != "" && (rec.Question != expected || rec.Family != family) {
			return errors.New("the session changed while awaiting the placement answer")
		}
		oldFamily, oldTarget := rec.Family, rec.Target
		defer func() {
			rec.ObservePending = rec.ObservePending || rec.Family != oldFamily || rec.Target != oldTarget
			if observe {
				result.news, rec.ObservePending = rec.ObservePending, false
			}
		}()
		explicit := requested != ""
		wanted, model := requested, requested
		if explicit {
			model = explicitSessionModel(wanted, oldFamily)
		}
		if !explicit {
			rec.Family = family
			wanted, model = reporting.SessionLoginForFamily(family), family
			if rec.Wanted != "" && rec.Wanted != wanted {
				if reporting.SessionLoginForFamily(oldFamily) == rec.Wanted && oldFamily != "" {
					rec.Wanted = ""
				} else {
					wanted, model = rec.Wanted, rec.Wanted
				}
			}
		}
		if explicit || wanted != mythosFamily {
			rec.ResumeMythos = ""
		}
		target, identity, err := sessionDestination(view, rec, wanted, model, explicit)
		if err != nil {
			if explicit || expected != "" {
				return err
			}
			rec.Question = ""
			result.line = err.Error()
			rec.LastMoveDeferral = result.line
			return nil
		}
		if target.dir == "" {
			target.dir = rec.Target
		}
		if target.dir != rec.Target && !explicit && target.allocation != "" {
			fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{rec.Token, rec.Target, model, target.dir, target.number, identity, target.name, target.allocation}, "\x00"))))
			prefix := fingerprint + "/"
			if expected != "" && !strings.HasPrefix(expected, prefix) {
				return errors.New("the account or allocation changed while awaiting the placement answer")
			}
			if !(strings.HasPrefix(rec.Question, prefix) && strings.HasSuffix(rec.Question, "/approved")) {
				result.line = "Waiting for the allocation decision for this " + model + " session."
				if !strings.HasPrefix(rec.Question, prefix) {
					rec.Question = prefix + rand.Text()
				}
				if reason := allocationDeferral(rec.Question, model); reason != "" {
					result.line = reason
				} else {
					result.question = &sessionQuestion{
						id: rec.ID, path: filepath.Join(view.BackupDir(), "links", rec.Token), model: reportedModel, placementModel: model, claim: rec.Question, actor: actor,
						text: fmt.Sprintf("Use %s’s shared quota for this %s session? It is allocated to %s.", termsafe.Strip(target.name), model, target.allocation),
					}
				}
				rec.LastMoveDeferral = result.line
				return nil
			}
		} else if expected != "" {
			return errors.New("the placement changed while awaiting the answer")
		}
		rec.Target, rec.Question, rec.ResumeMythos = target.dir, "", ""
		if explicit {
			rec.Wanted = requested
		}
		if rec.Wanted != reporting.SessionLoginForFamily(rec.Family) {
			result.line = modelHint(rec.Wanted)
		}
		if explicit && oldTarget == rec.Target {
			label, _ := reporting.SessionLoginOwner(view, rec.Target)
			result.line = strings.TrimSpace("Session is already on the " + label + ". " + result.line)
		}
		return nil
	}, repair...)
	return result, err
}

func allocationDeferral(claim, model string) string {
	switch {
	case strings.HasSuffix(claim, "/declined"):
		return "The allocation decision deferred this " + model + " session move."
	case strings.HasSuffix(claim, "/unanswered"):
		return "No allocation decision was received for this " + model + " session move."
	}
	return ""
}

func answerSessionQuestion(sw *core.Switcher, q sessionQuestion, granted bool, answerErr error) error {
	answer := "/approved"
	if answerErr != nil {
		answer = "/unanswered"
	} else if !granted {
		answer = "/declined"
	}
	err := reporting.MoveSessionLink(sw.Store, q.actor, q.id, q.path, false, func(rec *reporting.SessionLink) error {
		if rec.Question != q.claim || rec.Family != reporting.ClaudeFamily(q.model) {
			return errors.New("the session changed while awaiting the placement answer")
		}
		rec.Question += answer
		if reason := allocationDeferral(answer, q.placementModel); reason != "" {
			rec.LastMoveDeferral = reason
		}
		return nil
	})
	if err != nil || answer != "/approved" {
		return err
	}
	_, err = evaluateSessionPlacement(sw, q.id, q.path, q.model, "", false, q.claim+answer, q.actor)
	return err
}

func sessionDestination(view *store.Store, rec *reporting.SessionLink, wanted, model string, explicit bool) (mythosPlacement, string, error) {
	root, source := view.BackupDir(), rec.Target
	var target mythosPlacement
	if wanted == mythosFamily {
		var err error
		if rec.ResumeMythos != "" {
			set, err := loadStartSettings(root)
			if err != nil {
				return target, "", err
			}
			target, err = chooseMythosLogin(view, model, mythosLoginRequest{bound: rec.ResumeMythos}, set)
			if !errors.Is(err, errNoMythosQuota) {
				return target, target.dir, err
			}
			// An exhausted original login yields to the normal Mythos selection.
		}
		if isMythosDirectory(root, source) && !explicit {
			set, err := loadStartSettings(root)
			if err != nil {
				return target, "", err
			}
			target, err = mythosTickPlacement(view, source, set, false)
			return target, target.dir, err
		}
		target, err = mythosMovePlacement(view)
		return target, target.dir, err
	}
	target.dir = view.DefaultProfileDir()
	if wanted == "fable" {
		if source != paths.FableStoreDir(root) {
			// A running session cannot take ownership of a login reserved for native recovery.
			parked, err := store.FableAllocationParked(root)
			if err != nil {
				return target, "", err
			}
			readers, err := store.ReadFableReaders(root)
			if err != nil {
				return target, "", err
			}
			if parked || readers.Identity != "" {
				return target, "", cerr.Validation("the Fable login is reserved for a parked or recovered native session")
			}
		}
		st := view.FableStore()
		if st.Usable {
			_, toDefault, err := fableStartsOnDefault(&core.Switcher{Store: view}, "")
			if err != nil {
				return target, "", err
			}
			if !toDefault {
				target.dir = paths.FableStoreDir(root)
				view, err = view.ForFable()
				if err != nil {
					return target, "", err
				}
			} else if explicit || rec.Wanted == "fable" {
				return target, "", cerr.Validation(noFableStartLogin)
			}
		} else if explicit || rec.Wanted == "fable" {
			return target, "", cerr.Validation("the Fable store has no usable login (%s)", st.Reason)
		}
	}
	if source == target.dir && !explicit {
		return target, "", nil
	}
	data, err := view.SequenceForUpdate()
	if err != nil {
		return target, "", err
	}
	owner := view.ResolveOwnerSlot(data)
	if owner.Slot == "" || view.AccountKindFor(owner.Slot) == "oauth" && (owner.Source == store.SourceConfig || owner.Source == store.SourceLastInstall) {
		return target, "", cerr.Validation("cannot verify the current %s login for the session", wanted)
	}
	num, email, org, err := view.ResolveAccountFrom(data, owner.Slot)
	if err != nil {
		return target, "", err
	}
	if store.DisabledFrom(data, num) {
		return target, "", cerr.Validation("Account-%s is disabled", num)
	}
	id := usage.Identity{Email: email, OrgUUID: org}
	entry := view.Usage.Entries(map[string]usage.Identity{num: id})[num]
	if limits, _ := modelLimitWindows(entry, model, view.Clk.Now()); len(limits) > 0 {
		return target, "", cerr.Validation("Account-%s has no remaining quota for this session", num)
	}
	target.number, target.name = num, store.AccountNameFrom(data, num)
	if models, set := store.ModelsFrom(data, num); set && (model == "opus" || model == "fable") && !slices.Contains(models, model) {
		target.allocation = strings.Join(models, ", ")
	}
	return target, store.AccountIdentityDigest(email, org), nil
}

func checkLinkedSession(id, model, actor string) string {
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return err.Error()
	}
	result, err := placeSession(sw, id, sessionLinkPath, model, "", false, actor)
	if err != nil {
		return err.Error()
	}
	return result.line
}

func explicitSessionModel(store, family string) string {
	if store != "default" {
		return store
	}
	if reporting.SessionLoginForFamily(family) == "default" {
		return family
	}
	return ""
}

// sessionMoveChoices previews explicit use against the current binding. The command
// checks it again under both store locks when the operator chooses a destination.
func sessionMoveChoices(sw *core.Switcher, id string) ([]string, error) {
	if runtime.GOOS != "linux" || !sessionUUID.MatchString(id) {
		return nil, cerr.AccountNotFound("session has no linked login binding")
	}
	view := sw.Store.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	rec, err := reporting.InspectSessionLink(view.BackupDir(), id, view.Clk.Now())
	if err != nil {
		return nil, err
	}
	rec.ResumeMythos = ""
	choices := []string{}
	var diagnostics, reasons []error
	seen := make(map[string]bool)
	for _, dest := range []string{"mythos", "fable", "default"} {
		target, _, err := sessionDestination(view, &rec, dest, explicitSessionModel(dest, rec.Family), true)
		if err == nil {
			if target.dir == rec.Target {
				continue
			}
			err = reporting.SessionTargetAvailable(view.BackupDir(), rec.Target, target.dir)
		}
		if err != nil {
			if errors.Is(err, reporting.ErrSessionRefreshing) {
				return nil, err // The source lock blocks every destination.
			}
			if seen[err.Error()] {
				continue
			}
			seen[err.Error()] = true
			reasons = append(reasons, err)
			if cerr.TypeName(err) != string(cerr.KindValidation) && !errors.Is(err, errNoMythosQuota) && !errors.Is(err, errNoMythosLogin) {
				diagnostics = append(diagnostics, err)
			}
			continue
		}
		choices = append(choices, dest)
	}
	if len(choices) == 0 {
		return choices, errors.Join(reasons...)
	}
	return choices, errors.Join(diagnostics...)
}
