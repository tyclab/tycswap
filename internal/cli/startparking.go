package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

const recoveredFableEnv = "TYCSWAP_RECOVERED_FABLE_IDENTITY"

func recoveredFableStart(root, model, account string, args []string, native bool) (string, error) {
	var bound string
	if session := resumedSession(args); sessionUUID.MatchString(session) {
		scope, err := reporting.SessionBinding(root, session)
		if err != nil {
			return "", err
		}
		if digest, ok := strings.CutPrefix(scope, "fable/"); ok {
			if !validMythosDigest(digest) {
				return "", fmt.Errorf("invalid saved Fable session identity")
			}
			bound = digest
		}
	}
	if model != "fable" && bound == "" {
		return "", nil
	}
	if bound != "" && model != "" && model != "fable" {
		return "", fmt.Errorf("the recovered Fable session must resume on its original Fable login")
	}
	parked, err := store.FableAllocationParked(root)
	if err != nil {
		return "", err
	}
	readers, err := store.ReadFableReaders(root)
	if err != nil {
		return "", err
	}
	if !parked && readers.Identity == "" && bound == "" {
		return "", nil
	}
	if !native {
		return "", fmt.Errorf("the parked or recovered Fable login requires a native Claude launch")
	}
	if problem := startStoreProblem(root); problem != "" {
		return "", fmt.Errorf("cannot verify the recovered Fable store at %s", root)
	}
	if !freshNativeLaunch(args) && bound == "" {
		return "", fmt.Errorf("the Fable login is parked or recovered; this resume has no verified original login")
	}
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return "", err
	}
	return lifecycle.PrepareRecoveredFableStart(sw.Store, account, bound)
}
