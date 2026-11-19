// tycswap mythos marks the accounts that run Claude Mythos (DESIGN A106).
package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/tyclab/tycswap/internal/store"
)

// mythosCommand is `tycswap mythos --allow ID | --disallow ID | --list`, with
// the forms and exit codes of start's management forms: 0 done, 1 a store or
// account error, 2 a usage error.
func mythosCommand(prog string, argv []string, streams ioStreams) int {
	action, id := "", ""
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		switch tok {
		case "-h", "--help":
			return renderMythosHelp(prog, streams.out)
		case "--allow", "--disallow":
			if i+1 == len(argv) || argv[i+1] == "" || strings.HasPrefix(argv[i+1], "--") {
				return subError(prog+" mythos", streams.err, tok+" requires an account identifier")
			}
			if action != "" {
				return subError(prog+" mythos", streams.err, "options are exclusive")
			}
			i++
			action, id = tok, argv[i]
		case "--list":
			if action != "" {
				return subError(prog+" mythos", streams.err, "options are exclusive")
			}
			action = tok
		default:
			if strings.HasPrefix(tok, "-") {
				return subError(prog+" mythos", streams.err, "unrecognized option: "+tok)
			}
			return subError(prog+" mythos", streams.err, "unexpected argument: "+tok)
		}
	}
	if action == "" {
		return subError(prog+" mythos", streams.err, "one of --allow ID, --disallow ID or --list is required")
	}
	if code, blocked := guardRoot(streams.err); blocked {
		return code
	}
	sw, err := constructSwitcher(false, streams.err)
	if err != nil {
		mythosNotice(streams.err, err.Error())
		return 1
	}
	if action == "--list" {
		data, err := sw.SequenceForUpdate()
		if err != nil {
			mythosNotice(streams.err, err.Error())
			return 1
		}
		for _, n := range data.Sequence {
			if num := strconv.Itoa(n); store.MythosFrom(data, num) {
				fmt.Fprintln(streams.out, num)
			}
		}
		return 0
	}
	if err := sw.SetMythos(id, action == "--allow"); err != nil {
		mythosNotice(streams.err, err.Error())
		return 1
	}
	return 0
}

func mythosNotice(stderr io.Writer, message string) {
	fmt.Fprintln(stderr, "tycswap mythos: "+strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message))
}

func renderMythosHelp(prog string, out io.Writer) int {
	fmt.Fprintf(out, "usage: %s mythos --allow ID | --disallow ID | --list\n", prog)
	fmt.Fprintln(out, "\nMark the accounts that may run Claude Mythos; nothing tycswap reads tells them apart.")
	fmt.Fprintln(out, "While a session runs Mythos, auto-switch keeps the shared login on a marked account,")
	fmt.Fprintln(out, "and start starts a Mythos session on one. With no account marked nothing is restricted.")
	return 0
}
