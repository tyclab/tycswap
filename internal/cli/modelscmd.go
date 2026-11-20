package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/store"
)

// modelsArguments are `tycswap models`' forms: --list [--json], ID SET and
// ID --derived.
type modelsArguments struct {
	list, json, derived, help bool
	id                        string
	set                       []string
	hasSet                    bool
}

func parseModelsArguments(argv []string) (modelsArguments, error) {
	var parsed modelsArguments
	var positional []string
	for _, tok := range argv {
		switch tok {
		case "--list":
			parsed.list = true
		case "--json":
			parsed.json = true
		case "--derived":
			parsed.derived = true
		case "-h", "--help":
			parsed.help = true
		default:
			if strings.HasPrefix(tok, "-") {
				return parsed, fmt.Errorf("unrecognized option: %s", tok)
			}
			positional = append(positional, tok)
		}
	}
	if parsed.help {
		return parsed, nil
	}
	if len(positional) > 2 {
		return parsed, fmt.Errorf("unrecognized arguments: %s", strings.Join(positional[2:], " "))
	}
	if len(positional) > 0 {
		parsed.id = positional[0]
	}
	if len(positional) == 2 {
		set, err := store.ParseModels(positional[1])
		if err != nil {
			return parsed, err
		}
		parsed.set, parsed.hasSet = set, true
	}
	switch {
	case parsed.list && parsed.id != "":
		return parsed, fmt.Errorf("--list takes no account")
	case parsed.derived && parsed.id == "":
		return parsed, fmt.Errorf("--derived goes with an account")
	case !parsed.list && parsed.id == "":
		return parsed, fmt.Errorf("expected --list, ID SET or ID --derived")
	case parsed.hasSet && parsed.derived:
		return parsed, fmt.Errorf("a model set and --derived exclude each other")
	case parsed.id != "" && !parsed.hasSet && !parsed.derived:
		return parsed, fmt.Errorf("%s needs a model set (mythos, fable, opus) or --derived", parsed.id)
	case parsed.json && !parsed.list:
		return parsed, fmt.Errorf("--json goes with --list")
	}
	return parsed, nil
}

// modelsCommand handles `tycswap models ...`; argv excludes "models".
func modelsCommand(prog string, argv []string, s ioStreams) int {
	parsed, err := parseModelsArguments(argv)
	if err != nil {
		return subError(prog+" models", s.err, err.Error())
	}
	if parsed.help {
		return renderModelsHelp(prog, s.out)
	}
	sw, err := constructSwitcher(false, s.err)
	if err != nil {
		return renderDomainError(err, parsed.json, s.out, s.err)
	}
	if code, blocked := guardRoot(s.err); blocked {
		return code
	}
	setSigintJSON(parsed.json)
	switch {
	case parsed.derived:
		err = lifecycle.ClearModels(sw.Store, parsed.id)
	case parsed.hasSet:
		err = lifecycle.SetModels(sw.Store, parsed.id, parsed.set)
	default:
		return listModels(sw, parsed.json, s)
	}
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	return 0
}

type modelsRow struct {
	number, email     string
	set               []string
	explicit, applies bool
}

func listModels(sw *core.Switcher, jsonOut bool, s ioStreams) int {
	data, err := sw.ReadSequence()
	if err != nil {
		return renderDomainError(err, jsonOut, s.out, s.err)
	}
	var rows []modelsRow
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		_, email, _, err := sw.ResolveAccountFrom(data, num)
		if err != nil {
			continue
		}
		set, explicit := store.ModelsFrom(data, num)
		rows = append(rows, modelsRow{number: num, email: email, set: set, explicit: explicit, applies: explicit})
	}
	if jsonOut {
		writeJSONIndent(s.out, modelsListJSON(rows))
		return 0
	}
	w := tabwriter.NewWriter(s.out, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		source, note := "default", ""
		if r.explicit {
			source = "explicit"
		}
		if !r.applies {
			note = "(no explicit allocation)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.number, r.email, strings.Join(r.set, ", "), source, note)
	}
	_ = w.Flush()
	return 0
}

func modelsListJSON(rows []modelsRow) map[string]any {
	accounts := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		n, _ := strconv.Atoi(r.number)
		source := "default"
		if r.explicit {
			source = "explicit"
		}
		accounts = append(accounts, map[string]any{"number": n, "email": r.email, "models": r.set, "source": source, "applies": r.applies})
	}
	return map[string]any{"schemaVersion": jsonout.SchemaVersion, "accounts": accounts}
}

func renderModelsHelp(prog string, out io.Writer) int {
	fmt.Fprintf(out, "usage: %s models --list [--json] | ID SET | ID --derived\n", prog)
	fmt.Fprintln(out, "Choose the preferred model families for this account. Use --derived to clear the allocation.")
	fmt.Fprintln(out, "Native model access is determined by the provider when the session starts.")
	return 0
}
