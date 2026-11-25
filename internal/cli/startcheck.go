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
	"github.com/tyclab/tycswap/internal/termsafe"
)

// The status line places linked sessions and keeps the original warnings for direct stores.
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
	if sessionLinkPath != "" {
		if line := checkLinkedSession(session, model, "status line"); line != "" {
			fmt.Fprintln(out, termsafe.Strip(line))
		}
		return
	}
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
	on, intended := "the default login", "the Fable store"
	if fableSessionProcess {
		on, intended = intended, on
	}
	name := strings.ToUpper(family[:1]) + family[1:]
	line := "⚠ " + name + " session on " + on
	if !fableSessionProcess {
		if cause := fableToDefaultCause(); cause != "" {
			line += " (" + cause + ")"
		}
	}
	fmt.Fprintln(out, line+". Use "+intended+" with an account approved for "+name+".")
}

// fableToDefaultCause is why start sends a Fable session to the default login
// although the Fable store is usable, as start decides it; "" when it does
// not (DESIGN A105, A112).
func fableToDefaultCause() string {
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return ""
	}
	_, toDefault, err := fableStartsOnDefault(sw, "")
	switch {
	case err != nil:
		return ""
	case toDefault:
		return noFableStartLogin
	}
	if byRoom, _, _ := fableSessionOnDefault(sw.Store); byRoom {
		return noFableRoom
	}
	return ""
}
