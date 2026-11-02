package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestRoutingMalformedRequestsReturnJSON(t *testing.T) {
	for _, input := range []string{"", "{} {}", `{"pid":"123"}`, `{"unexpected":true}`} {
		var out, stderr bytes.Buffer
		code := routingCommand("tycswap", []string{"acquire", "--group", "fable"}, ioStreams{in: strings.NewReader(input), out: &out, err: &stderr})
		if code == 0 || !json.Valid(out.Bytes()) {
			t.Fatalf("input %q: code=%d stdout=%s stderr=%s", input, code, out.String(), stderr.String())
		}
	}
}

func TestManagedModelGuardRequiresRegisteredProcess(t *testing.T) {
	testutil.IsolateHome(t)
	t.Setenv("TYCSWAP_ROUTE_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("TYCSWAP_ROUTE_LAUNCH", "launch")
	if err := os.MkdirAll(paths.GetBackupRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := managedModelGuard(groups.Fable, "session", "opus"); err == nil {
		t.Fatal("managed model guard accepted an unregistered process")
	}
	if err := routingstate.With(paths.GetBackupRoot(), func(state *routingstate.State) error {
		state.Sessions["fable/session"] = routingstate.Session{Group: "fable", ID: "session", PID: os.Getpid(), Launch: "launch", Model: "fable"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := managedModelGuard(groups.Fable, "session", "opus"); err != nil {
		t.Fatal(err)
	}
	if err := managedModelGuard(groups.Fable, "session", "unknown"); err == nil {
		t.Fatal("unknown model accepted")
	}
	t.Setenv("TYCSWAP_ROUTE_LAUNCH", "foreign")
	if err := managedModelGuard(groups.Fable, "session", "opus"); err == nil {
		t.Fatal("foreign launch bypassed the group guard")
	}
}
