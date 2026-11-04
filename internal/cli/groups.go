package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/recovery"
)

// groupsCommand answers the hook a still-running session of a retired group
// calls before a model switch; every other verb is gone (DESIGN A78).
func groupsCommand(prog string, args []string, streams ioStreams) int {
	if len(args) == 0 || args[0] != "guard" {
		errorTo(streams.err, "Error: session groups are retired; "+prog+" switches the default login by the models its sessions run")
		return 1
	}
	if len(args) != 3 || args[1] != "--group" {
		return subError(prog+" groups guard", streams.err, "expected --group fable|opus")
	}
	id, err := groups.Parse(args[2])
	var event struct {
		ToModel   string `json:"to_model"`
		FromModel string `json:"from_model"`
		SessionID string `json:"session_id"`
	}
	if err == nil {
		err = json.NewDecoder(io.LimitReader(streams.in, 1<<20)).Decode(&event)
	}
	if err == nil {
		err = groups.CheckModel(id, event.ToModel)
	}
	if err == nil && event.SessionID != "" {
		err = recovery.NewStore(paths.GetBackupRoot()).ModelChange(event.SessionID, event.FromModel, event.ToModel, time.Now())
	}
	if err != nil {
		fmt.Fprintln(streams.err, err)
		return 2
	}
	return 0
}
