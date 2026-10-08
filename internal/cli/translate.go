// translate.go — memorable-verb → legacy-flag rewrite (spec 08§2).
//
// Implements spec 08§2 (_translate_subcommand + _SUBCOMMAND_FLAGS). The first
// argv token, when a recognized verb, is rewritten into the hidden legacy
// --flag interface the main parser actually understands; tokens after the verb
// pass through verbatim so --json/--strategy/--slot/--force keep combining.
// run/auto/config/map/unmap/alias/swap/move are NOT here — they are
// pre-dispatched (spec 08§1) and never reach translation.
package cli

var subcommandFlags = map[string]string{
	"help":      "--help",
	"list":      "--list",
	"ls":        "--list",
	"status":    "--status",
	"add":       "--add-account",
	"add-token": "--add-token",
	"remove":    "--remove-account",
	"rm":        "--remove-account",
	"disable":   "--disable-account",
	"enable":    "--enable-account",
	"export":    "--export",
	"import":    "--import",
	"purge":     "--purge",
	"upgrade":   "--upgrade",
	"update":    "--upgrade",
	"tui":       "--tui",
	"watch":     "--watch",
	"menubar":   "--menubar",
}

func translateSubcommand(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	verb, rest := argv[0], argv[1:]

	if verb == "switch" {
		// Bare `switch` rotates; `switch <num|email>` jumps to that account.
		if len(rest) > 0 && !startsWithDash(rest[0]) {
			return append([]string{"--switch-to"}, rest...)
		}
		return append([]string{"--switch"}, rest...)
	}

	if flag, ok := subcommandFlags[verb]; ok {
		return append([]string{flag}, rest...)
	}
	return argv
}

func startsWithDash(s string) bool {
	return len(s) > 0 && s[0] == '-'
}
