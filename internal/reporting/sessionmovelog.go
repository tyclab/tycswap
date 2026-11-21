package reporting

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
)

type sessionMoveLog struct {
	view  *store.Store
	actor string
}

// outcome runs after the commit attempt. A failed proposal must preserve the
// verified source and its metadata, including during an explicit use retry.
func (log sessionMoveLog) outcome(root string, records map[string]defaultSessionModel, old defaultSessionModel, before, after SessionLink, err error) error {
	id := before.ID[:min(8, len(before.ID))]
	reason := after.LastMoveDeferral
	if err != nil {
		reason = termsafe.Strip(strings.Join(strings.Fields(err.Error()), " "))
		if cerr.TypeName(err) == string(cerr.KindClaudeCodeLockTimeout) {
			// Age changes between samples. The held lock is still the same reason.
			reason = "source login is refreshing (.oauth_refresh.lock)"
		}
		if reason != before.LastMoveDeferral {
			beforeCopy := before
			beforeCopy.LastMoveDeferral = reason
			old.Link = &beforeCopy
			records[before.ID] = old
			if saveErr := saveSessionModels(root, records); saveErr != nil {
				return errors.Join(err, saveErr)
			}
		}
	} else if before.Target != after.Target {
		log.view.Log.Action(fmt.Sprintf("Session %s moved from %s to %s", id, log.login(before.Target), log.login(after.Target)), log.actor)
		return nil
	}
	if reason != "" && reason != before.LastMoveDeferral {
		log.view.Log.Infof("Session %s move deferred by %s: %s", id, log.actor, termsafe.Strip(reason))
	}
	return err
}

func (log sessionMoveLog) login(target string) string {
	label, num := SessionLoginOwner(log.view, target)
	if num != "unknown" {
		num = "Account-" + num
	}
	return label + " (" + num + ")"
}
