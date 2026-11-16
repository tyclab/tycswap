package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/core"
)

// cliActor names a command run from a shell: this process and, where /proc
// exists, the program that started it.
func cliActor() string {
	actor := "cli(pid=" + strconv.Itoa(os.Getpid())
	if comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(os.Getppid()), "comm")); err == nil {
		actor += ",parent=" + strings.TrimSpace(string(comm))
	}
	return actor + ")"
}

// rosterMark is what every switch writes to the roster: the active slot and
// the roster's stamp. A human-mode switch returns no payload, so a change of
// mark is how its caller learns that it switched.
type rosterMark struct{ active, stamp string }

func markOf(sw *core.Switcher) rosterMark {
	data, err := sw.ReadSequence()
	if err != nil || data == nil {
		return rosterMark{}
	}
	m := rosterMark{stamp: data.LastUpdated}
	if data.ActiveAccountNumber != nil {
		m.active = strconv.Itoa(*data.ActiveAccountNumber)
	}
	return m
}

// logSwitchSince writes the actor line of a switch that changed the roster's
// mark since before.
func logSwitchSince(sw *core.Switcher, before rosterMark, actor string) {
	if after := markOf(sw); after != before && after.active != "" {
		sw.Log.Action("Switched to account "+after.active, actor)
	}
}

func enabledWord(disabled bool) string {
	if disabled {
		return "Disabled"
	}
	return "Enabled"
}
