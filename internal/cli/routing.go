package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/routing"
	"github.com/tyclab/tycswap/internal/routingstate"
)

func routingCommand(prog string, args []string, streams ioStreams) int {
	fail := func(err error) int { return renderDomainError(err, true, streams.out, streams.err) }
	if len(args) != 3 || args[1] != "--group" {
		return fail(fmt.Errorf("expected %s routing acquire|release|register|unregister --group fable|opus", prog))
	}
	id, err := groups.Parse(args[2])
	if err != nil {
		return fail(err)
	}
	switch args[0] {
	case "acquire", "release", "register", "unregister":
	default:
		return fail(fmt.Errorf("unknown routing operation %q", args[0]))
	}
	var request routing.Request
	decoder := json.NewDecoder(io.LimitReader(streams.in, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fail(fmt.Errorf("invalid routing request: %w", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fail(fmt.Errorf("routing expects exactly one JSON request"))
	}
	sw, err := constructSwitcher(false, streams.err)
	if err != nil {
		return fail(err)
	}
	s, err := sw.Store.ForGroup(id)
	if err != nil {
		return fail(err)
	}
	coordinator := routing.Coordinator{Store: s}
	var result routing.Result
	switch args[0] {
	case "acquire":
		result, err = coordinator.Acquire(request)
	case "release":
		result, err = coordinator.Release(request)
	case "register":
		result, err = coordinator.Register(request)
	case "unregister":
		result, err = coordinator.Unregister(request)
	}
	if err != nil {
		return fail(err)
	}
	writeJSONCompact(streams.out, result)
	return 0
}

func managedModelGuard(group groups.ID, session, model string) error {
	if _, err := routingstate.Family(model); err != nil {
		return err
	}
	pid, err := strconv.Atoi(os.Getenv("TYCSWAP_ROUTE_PID"))
	if err != nil || pid <= 1 {
		return fmt.Errorf("managed routing process is missing")
	}
	return routingstate.With(paths.GetBackupRoot(), func(state *routingstate.State) error {
		registered, ok := state.Sessions[routingstate.Key(string(group), session)]
		if !ok || registered.PID != pid || registered.Launch == "" || registered.Launch != os.Getenv("TYCSWAP_ROUTE_LAUNCH") {
			return fmt.Errorf("managed routing session is not registered")
		}
		return nil
	})
}
