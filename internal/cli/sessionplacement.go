package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"

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
		result, err = placeSession(sw, id, path, "", args[0], false)
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
	line string
	news bool
	// The status line can wait for its own question after the tick and outside both locks.
	ask func() error
}

func placeSession(sw *core.Switcher, id, path, reportedModel, requested string, observe bool) (sessionMoveResult, error) {
	var result sessionMoveResult
	family := reporting.ClaudeFamily(reportedModel)
	if requested == "" && (strings.TrimSpace(reportedModel) == "" || !sessionUUID.MatchString(id)) {
		return result, nil
	}
	view := sw.Store.WithoutLineageWrites()
	view.OAuth, view.Log = nil, nil
	var claim, question, contextID, approved string
	update := func() error {
		return reporting.UpdateSessionLink(view.BackupDir(), id, path, view.Clk.Now(), func(rec *reporting.SessionLink) error {
			if approved != "" && (rec.Question != approved || rec.Family != family) {
				return errors.New("the session changed while awaiting the placement answer")
			}
			oldFamily, oldTarget := rec.Family, rec.Target
			defer func() {
				rec.ObservePending = rec.ObservePending || rec.Family != oldFamily || rec.Target != oldTarget
				if observe && approved == "" {
					result.news, rec.ObservePending = rec.ObservePending, false
				}
			}()
			explicit := requested != ""
			wanted, model := requested, requested
			if explicit && wanted == "default" {
				model = ""
				if reporting.SessionLoginForFamily(oldFamily) == "default" {
					model = oldFamily
				}
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
				if explicit || approved != "" {
					return err
				}
				rec.Question = ""
				result.line = err.Error()
				return nil
			}
			if target.dir == "" {
				target.dir = rec.Target
			}
			if target.dir != rec.Target && !explicit && target.allocation != "" {
				fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{rec.Token, rec.Target, model, target.dir, target.number, identity, target.name, target.allocation}, "\x00"))))
				prefix := fingerprint + "/"
				if approved != "" && !strings.HasPrefix(approved, prefix) {
					return errors.New("the account or allocation changed while awaiting the placement answer")
				}
				if !(strings.HasPrefix(rec.Question, prefix) && strings.HasSuffix(rec.Question, "/approved")) {
					result.line = "Waiting for the allocation decision for this " + model + " session."
					switch {
					case !strings.HasPrefix(rec.Question, prefix):
						claim = prefix + rand.Text()
						rec.Question = claim
						question = fmt.Sprintf("Use %s’s shared quota for this %s session? It is allocated to %s.", termsafe.Strip(target.name), model, target.allocation)
						contextID = rec.ID
					case strings.HasSuffix(rec.Question, "/declined"):
						result.line = "The allocation decision deferred this " + model + " session move."
					case strings.HasSuffix(rec.Question, "/unanswered"):
						result.line = "No allocation decision was received for this " + model + " session move."
					}
					return nil
				}
			} else if approved != "" {
				return errors.New("the placement changed while awaiting the answer")
			}
			rec.Target, rec.Question, rec.ResumeMythos = target.dir, "", ""
			if explicit {
				rec.Wanted = requested
			}
			result.line = modelHint(rec.Wanted)
			return nil
		})
	}
	err := update()
	if err == nil && claim != "" {
		result.ask = func() error {
			ok, answerErr := askPlacement(ioStreams{err: io.Discard}, question, contextID, true)
			answer := "/approved"
			if answerErr != nil {
				answer = "/unanswered"
			} else if !ok {
				answer = "/declined"
				answerErr = errors.New("session move deferred by the allocation decision")
			}
			// Keep the answer if a client refresh defers the move to a later observation.
			err := reporting.UpdateSessionLink(view.BackupDir(), id, path, view.Clk.Now(), func(rec *reporting.SessionLink) error {
				if rec.Question != claim || rec.Family != family {
					return errors.New("the session changed while awaiting the placement answer")
				}
				rec.Question += answer
				return nil
			})
			if err != nil {
				return err
			}
			if answerErr != nil {
				return answerErr
			}
			approved = claim + answer
			return update()
		}
	}
	return result, err
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
			target, err = chooseMythosLogin(view, model, "", rec.ResumeMythos, "", "", set)
			if !errors.Is(err, errNoMythosQuota) {
				return target, target.dir, err
			}
			// An exhausted original login yields to the normal Mythos selection.
		}
		if isMythosDirectory(root, source) && !explicit {
			set, err := loadStartSettings(root)
			if err != nil || !set.Enabled {
				return target, "", err
			}
			target, err = mythosTickPlacement(view, source, set)
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
				return target, "", errors.New("the Fable login is reserved for a parked or recovered native session")
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
				return target, "", errors.New(noFableStartLogin)
			}
		} else if explicit || rec.Wanted == "fable" {
			return target, "", fmt.Errorf("the Fable store has no usable login (%s)", st.Reason)
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
		return target, "", fmt.Errorf("cannot verify the current %s login for the session", wanted)
	}
	num, email, org, err := view.ResolveAccountFrom(data, owner.Slot)
	if err != nil {
		return target, "", err
	}
	if store.DisabledFrom(data, num) {
		return target, "", fmt.Errorf("Account-%s is disabled", num)
	}
	id := usage.Identity{Email: email, OrgUUID: org}
	entry := view.Usage.Entries(map[string]usage.Identity{num: id})[num]
	if room := startModelHeadroom(entry, model, view.Clk.Now()); room != nil && *room <= 0 {
		return target, "", fmt.Errorf("Account-%s has no remaining quota for this session", num)
	}
	target.number, target.name = num, store.AccountNameFrom(data, num)
	if models, set := store.ModelsFrom(data, num); set && (model == "opus" || model == "fable") && !slices.Contains(models, model) {
		target.allocation = strings.Join(models, ", ")
	}
	return target, store.AccountIdentityDigest(email, org), nil
}

func checkLinkedSession(id, model string) string {
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return err.Error()
	}
	result, err := placeSession(sw, id, sessionLinkPath, model, "", false)
	if err != nil {
		return err.Error()
	}
	if result.ask != nil {
		if err := result.ask(); err != nil {
			return err.Error()
		}
		return ""
	}
	return result.line
}
