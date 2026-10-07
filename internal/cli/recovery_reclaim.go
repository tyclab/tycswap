package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/recovery"
)

func reclaimHandover(store *recovery.Store, cwd string, backgroundConfirmed bool) error {
	state, err := store.Snapshot()
	if err != nil {
		return err
	}
	h, ok := state.Handovers[leaseWorkspace(cwd)]
	if !ok || h.Status != "active" || h.DestinationSessionID == "" {
		return errors.New("confirmed destination is required; reconcile the pending launch before reclaiming")
	}
	if !backgroundConfirmed {
		return errors.New("review background commands and use --confirm-no-background-tools before reclaiming")
	}
	if h.Destination.Provider == "claude" {
		group, err := groups.Parse(h.Destination.Group)
		if err != nil {
			return errors.New("destination profile is unknown")
		}
		live, err := strictProfileSessions(groups.ProfileDir(paths.GetBackupRoot(), group))
		if err != nil {
			return err
		}
		for _, session := range live {
			if session.SessionID == h.DestinationSessionID {
				return errors.New("destination is still running; stop it before reclaiming source editing ownership")
			}
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		data, err := exec.CommandContext(ctx, "flakelab", "sessions", "--json").Output()
		if err != nil {
			return fmt.Errorf("destination liveness is unknown: %w", err)
		}
		var live []launchedSession
		if err := json.Unmarshal(data, &live); err != nil {
			return errors.New("destination process discovery returned invalid data")
		}
		for _, session := range live {
			if session.Tool == h.Destination.Provider && session.Session == h.DestinationSessionID {
				return errors.New("destination is still running; stop it before reclaiming source editing ownership")
			}
		}
	}
	return store.Reclaim(cwd, h.PacketDigest, true, backgroundConfirmed)
}
