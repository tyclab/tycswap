// tycswap start --check, the status line's word on a session that runs on the
// other store than its model's (DESIGN A108).
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

// startCheck prints one line for the status line's session when it runs on
// the wrong store: a Fable session on the default login while the Fable store
// is usable, or another family's session on the Fable store; nothing else,
// and nothing under start's overrides. While start itself sends Fable
// sessions to the default login, the line names that cause. It reads the
// status line's JSON unless stdin is a terminal, and the Fable store as start
// reads it; it writes nothing and never fails.
func startCheck(in io.Reader, out io.Writer) {
	defer func() { _ = recover() }()
	root := paths.GetBackupRoot()
	if startOverride(os.Environ(), nil, filepath.Clean(paths.FableStoreDir(root))) != "" {
		return
	}
	if f, ok := in.(*os.File); ok && isTTY(f) {
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(in, 1<<20))
	session, model := observedSessionModel(raw)
	family := reporting.ClaudeFamily(model)
	if mythosSessionDir != "" {
		if family != "" && family != mythosFamily {
			fmt.Fprintf(out, "Claude reports %s on the independent Mythos login.\n", family)
		}
		return
	}
	if family == "" || fableSessionProcess == (family == "fable") {
		return
	}
	fable, hasFableLogins := startFableStore(root)
	if !hasFableLogins || !fableSessionProcess && !fable.Usable {
		return
	}
	on, to := "the default login", "the Fable store"
	if fableSessionProcess {
		on, to = to, on
	}
	line := "⚠ " + strings.ToUpper(family[:1]) + family[1:] + " session on " + on + ": "
	if !fableSessionProcess && sendsFableToDefault() {
		line += noFableStartLogin + "; mark one: tycswap start --allow ID"
	} else if sessionUUID.MatchString(session) {
		line += "/exit, then claude --resume " + session + " --model " + modelArg(model) + " puts it on " + to
	} else {
		line += "restart it with --model " + modelArg(model)
	}
	fmt.Fprintln(out, line)
}

// modelArg is the session's own model id for a shell, as ClaudeFamily read
// and validated it (lower case, letters, digits and dashes, an optional
// [1m]), single-quoted with [1m] so the shell does not take it as a pattern.
func modelArg(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if strings.HasSuffix(id, "[1m]") {
		return "'" + id + "'"
	}
	return id
}

// sendsFableToDefault reports whether start sends a Fable session to the
// default login although the Fable store is usable, as start decides it.
func sendsFableToDefault() bool {
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return false
	}
	_, toDefault, err := fableStartsOnDefault(sw, "")
	return err == nil && toDefault
}
