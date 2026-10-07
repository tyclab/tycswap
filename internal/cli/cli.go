package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

// ioStreams bundles the CLI's own I/O (the switcher's human output still goes
// to os.Stdout, matching Python). Injected so tests capture what the front
// controller itself writes.
type ioStreams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

// Main is the process entry point. cmd/tycswap does os.Exit(cli.Main()).
func Main() int {
	printer.ForceUTF8Output() // spec 08§1 step 1 (force_utf8_output)
	// _use_native_tls (step 2) is intentionally dropped — Go's crypto/tls uses
	// the platform verifier natively (DESIGN Deviation 4).

	prog := progName(os.Args[0])
	streams := ioStreams{in: os.Stdin, out: os.Stdout, err: os.Stderr}
	installSigint(streams)
	return run(prog, os.Args[1:], streams, isTTY(os.Stdin), isTTY(os.Stdout))
}

// run is the testable front controller. argv excludes the program name.
func run(prog string, argv []string, s ioStreams, stdinTTY, stdoutTTY bool) int {
	// D2 (FINDING 2): a shell pinned via `tycswap env` carries CLAUDE_CONFIG_DIR
	// pointing at a tycswap session profile. Every command EXCEPT `env`/`run`
	// (which own their own preset handling) should operate on the DEFAULT login,
	// not the pinned profile, so neutralize the pin here BEFORE any dispatch.
	hook := len(argv) > 1 && ((argv[0] == "groups" && argv[1] == "guard") || (argv[0] == "recovery" && argv[1] == "record"))
	if !hook && (len(argv) == 0 || (argv[0] != "run" && argv[0] != "env")) {
		neutralizePinnedSessionProfile(s.err)
	}

	printOldStoreHint(argv, s.err)

	// The store holds credentials and the lock file: before any command reads
	// or writes it, refuse one another local user owns and make one that is
	// writable by group or other private; a mode chmod cannot change is one
	// warning line on stderr.
	if !isHelpOrVersion(argv) {
		warning, err := paths.CheckPrivateRoot(paths.GetBackupRoot())
		if err != nil {
			errorTo(s.err, err.Error())
			return 1
		}
		if warning != "" {
			fmt.Fprintln(s.err, printer.Yellowed("warning: "+warning))
		}
	}

	// Pre-dispatch on the first token (spec 08§1 step 4). Each must be the
	// first argument (DESIGN Deviation 10): `tycswap --debug run 2` is unsupported.
	if len(argv) > 0 {
		switch argv[0] {
		case "run":
			return runCommand(prog, argv[1:], s)
		case "groups":
			return groupsCommand(prog, argv[1:], s)
		case "recovery":
			return recoveryCommand(prog, argv[1:], s)
		case "env":
			return envCommand(prog, argv[1:], s)
		case "auto":
			return autoCommand(prog, argv[1:], s)
		case "config":
			return configCommand(prog, argv[1:], s)
		case "map":
			return mapCommand(prog, argv[1:], s)
		case "unmap":
			return unmapCommand(prog, argv[1:], s)
		case "alias":
			return aliasCommand(prog, argv[1:], s)
		case "swap":
			return swapCommand(prog, argv[1:], s)
		case "move":
			return moveCommand(prog, argv[1:], s)
		case "codex":
			return codexCommand(prog, argv[1:], s)
		case "migrate":
			return migrateCommand(prog, argv[1:], s)
		case "web":
			return webCommand(prog, argv[1:], s)
		case "app":
			return appCommand(prog, argv[1:], s)
		}
	}

	// `tycswap add --login` runs a login before it stores anything; a plain add
	// falls through to the main parser (handled == false).
	if len(argv) > 0 && (argv[0] == "add" || argv[0] == "--add-account") {
		if code, handled := addCommand(prog, argv[1:], s); handled {
			return code
		}
	}

	if len(argv) == 0 && ((stdoutTTY && stdinTTY) || msysTerminal()) {
		return startBackgroundApp(prog, s)
	}

	// Memorable verbs → legacy flags (spec 08§1 step 6, §2).
	argv = translateSubcommand(argv)

	// Main flag parser (step 7): parse → cross-flag validate → dispatch.
	pr := parseArgs(prog, argv, s.out, s.err)
	if pr.done {
		return pr.code
	}
	if vr := crossFlagValidate(prog, pr.p, s.err); vr.done {
		return vr.code
	}
	setSigintJSON(pr.p.json)
	return dispatchMain(prog, pr.p, s)
}

// Restore the launching shell's default profile for commands outside a group.
func neutralizePinnedSessionProfile(stderr io.Writer) {
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if cfg == "" {
		return
	}
	if !sessprofile.IsSessionProfileDir(paths.GetBackupRoot(), cfg) {
		return
	}
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	if original := os.Getenv("TYCSWAP_DEFAULT_PROFILE_DIR"); os.Getenv("TYCSWAP_DEFAULT_PROFILE_UNPINNED") != "true" && filepath.IsAbs(original) && !sessprofile.IsSessionProfileDir(paths.GetBackupRoot(), original) {
		_ = os.Setenv("CLAUDE_CONFIG_DIR", original)
	}
	io.WriteString(stderr, printer.Dimmed("This shell is pinned via tycswap env; operating on the default login.")+"\n")
}

// msysTerminal reports a Git Bash / MSYS2 terminal on Windows. mintty hands
// programs pipes, not a console, so the TTY test calls a person typing the
// bare command there a script; MSYSTEM is what those shells set (A41). A
// seam for tests.
var msysTerminal = func() bool {
	return runtime.GOOS == "windows" && os.Getenv("MSYSTEM") != ""
}

// isTTY reports whether f is a character device (an interactive terminal).
// Uses stdlib os.FileInfo (no x/term dependency).
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// isHelpOrVersion reports whether argv only asks for help or the version,
// which touch no store.
func isHelpOrVersion(argv []string) bool {
	if len(argv) != 1 {
		return false
	}
	switch argv[0] {
	case "-h", "--help", "help", "--version", "version", "-V":
		return true
	}
	return false
}
