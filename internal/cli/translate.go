// translate.go — memorable-verb → legacy-flag rewrite (spec 08§2).
//
// Implements spec 08§2 (_translate_subcommand + _SUBCOMMAND_FLAGS). The first
// argv token, when a recognized verb, is rewritten into the hidden legacy
// --flag interface the main parser actually understands; tokens after the verb
// pass through verbatim so --json/--strategy/--slot/--force keep combining.
// run/auto/config/map/unmap/alias/swap/move are NOT here — they are
// pre-dispatched (spec 08§1) and never reach translation.
package cli

import "strings"

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
		// --store and its value go last, so the account may follow it; with
		// --store fable the account may also follow any other option.
		rest, store := splitStoreFlag(rest)
		if len(store) > 0 && strings.TrimPrefix(store[len(store)-1], "--store=") == "fable" {
			rest = accountFirst(rest)
		}
		if len(rest) > 0 && !startsWithDash(rest[0]) {
			return append(append([]string{"--switch-to"}, rest...), store...)
		}
		return append(append([]string{"--switch"}, rest...), store...)
	}

	if flag, ok := subcommandFlags[verb]; ok {
		return append([]string{flag}, rest...)
	}
	return argv
}

// splitStoreFlag takes `--store X` and `--store=X` out of a switch's
// arguments (DESIGN A100). A --store without a value stays for the parser.
func splitStoreFlag(args []string) (rest, store []string) {
	for i := 0; i < len(args); i++ {
		switch tok := args[i]; {
		case tok == "--":
			return append(rest, args[i:]...), store
		case strings.HasPrefix(tok, "--store="):
			store = append(store, tok)
		case tok == "--store" && i+1 < len(args) && !isOptionLike(args[i+1]):
			store = append(store, tok, args[i+1])
			i++
		default:
			rest = append(rest, tok)
		}
	}
	return rest, store
}

// switchValueFlags are the main parser's options that take a separate
// value: the token after one is that value, not an account.
var switchValueFlags = map[string]bool{"--strategy": true, "--slot": true, "--email": true, "--base-url": true, "--account": true, "--alias": true}

// accountFirst moves the first positional argument before "--" to the
// front, where the switch takes its account.
func accountFirst(args []string) []string {
	for i := 0; i < len(args); i++ {
		switch tok := args[i]; {
		case tok == "--":
			return args
		case switchValueFlags[tok]:
			i++
		case !startsWithDash(tok):
			return append(append([]string{tok}, args[:i]...), args[i+1:]...)
		}
	}
	return args
}

func startsWithDash(s string) bool {
	return len(s) > 0 && s[0] == '-'
}
