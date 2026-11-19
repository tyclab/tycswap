package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/store"
)

// fableRow is one account of `tycswap fable --list` (DESIGN A107).
type fableRow struct {
	Number, Email string
	Answer        store.FableAnswer
	Login         bool
}

// fableArguments are `tycswap fable`'s options: --allow and --disallow
// take an account, --list takes --json; the three exclude each other.
type fableArguments struct {
	action, id string
	json, help bool
}

func parseFableArguments(argv []string) (fableArguments, error) {
	var parsed fableArguments
	for i := 0; i < len(argv); i++ {
		switch tok := argv[i]; tok {
		case "--allow", "--disallow":
			if i+1 == len(argv) || argv[i+1] == "" || strings.HasPrefix(argv[i+1], "--") {
				return parsed, fmt.Errorf("%s requires an account identifier", tok)
			}
			if parsed.action != "" {
				return parsed, fmt.Errorf("--allow, --disallow and --list are exclusive")
			}
			i++
			parsed.action, parsed.id = tok, argv[i]
		case "--list":
			if parsed.action != "" {
				return parsed, fmt.Errorf("--allow, --disallow and --list are exclusive")
			}
			parsed.action = tok
		case "--json":
			parsed.json = true
		case "-h", "--help":
			parsed.help = true
		default:
			if strings.HasPrefix(tok, "-") {
				return parsed, fmt.Errorf("unrecognized option: %s", tok)
			}
			return parsed, fmt.Errorf("unrecognized arguments: %s", tok)
		}
	}
	switch {
	case parsed.help:
	case parsed.action == "":
		return parsed, fmt.Errorf("expected --allow ID, --disallow ID or --list")
	case parsed.json && parsed.action != "--list":
		return parsed, fmt.Errorf("--json goes with --list")
	}
	return parsed, nil
}

// fableCommand handles `tycswap fable ...`; argv excludes "fable".
func fableCommand(prog string, argv []string, s ioStreams) int {
	parsed, err := parseFableArguments(argv)
	if err != nil {
		return subError(prog+" fable", s.err, err.Error())
	}
	if parsed.help {
		return renderFableHelp(prog, s.out)
	}
	sw, err := constructSwitcher(false, s.err)
	if err != nil {
		return renderDomainError(err, parsed.json, s.out, s.err)
	}
	if code, blocked := guardRoot(s.err); blocked {
		return code
	}
	setSigintJSON(parsed.json)
	if parsed.action != "--list" {
		if err := sw.SetFableMark(parsed.id, parsed.action == "--allow"); err != nil {
			return renderDomainError(err, false, s.out, s.err)
		}
		return 0
	}
	rows, err := fableRows(sw)
	if err != nil {
		return renderDomainError(err, parsed.json, s.out, s.err)
	}
	if parsed.json {
		writeJSONIndent(s.out, fableListJSON(rows))
		return 0
	}
	w := tabwriter.NewWriter(s.out, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		login := "no"
		if r.Login {
			login = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\tFable: %s\tFable login: %s\n", r.Number, r.Email, fableReason(r.Answer), login)
	}
	_ = w.Flush()
	return 0
}

// fableRows is every roster account in order, with its Fable answer and
// whether it holds a Fable login.
func fableRows(sw *core.Switcher) ([]fableRow, error) {
	data, err := sw.ReadSequence()
	if err != nil {
		return nil, err
	}
	rows := make([]fableRow, 0, len(data.Sequence))
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		_, email, _, err := sw.ResolveAccountFrom(data, num)
		if err != nil {
			continue
		}
		rows = append(rows, fableRow{Number: num, Email: email,
			Answer: sw.Store.FableEntitlement(data, num), Login: sw.Store.HasFableLogin(num)})
	}
	return rows, nil
}

// fableReason says whether an account runs Fable and why.
func fableReason(a store.FableAnswer) string {
	yes := "no"
	if a.Capable {
		yes = "yes"
	}
	switch a.By {
	case store.FableByMark:
		if a.Capable {
			return "yes, marked"
		}
		return "no, marked off"
	case store.FableByPlan:
		return yes + ", plan " + a.Plan
	case store.FableAPIKey:
		return "no, API key"
	}
	if a.Plan != "" {
		return "no, unknown plan " + a.Plan
	}
	return "no, unknown plan"
}

func fableListJSON(rows []fableRow) map[string]any {
	accounts := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		n, _ := strconv.Atoi(r.Number)
		row := map[string]any{"number": n, "email": r.Email, "fable": r.Answer.Capable, "by": r.Answer.By, "fableLogin": r.Login}
		if r.Answer.Plan != "" {
			row["plan"] = r.Answer.Plan
		}
		accounts = append(accounts, row)
	}
	return map[string]any{"schemaVersion": jsonout.SchemaVersion, "accounts": accounts}
}

func renderFableHelp(prog string, out io.Writer) int {
	fmt.Fprintf(out, "usage: %s fable --allow ID | --disallow ID | --list [--json]\n", prog)
	fmt.Fprintln(out, "\nMark an account that runs Fable when its plan does not say so (an organization's login, say):")
	fmt.Fprintln(out, "--allow records it, --disallow removes the mark so the plan decides again. --list shows, per")
	fmt.Fprintln(out, "account, whether it runs Fable, by plan or mark, and whether it holds a Fable login.")
	fmt.Fprintln(out, "A Pro or Free plan cannot be marked, nor an API-key account.")
	return 0
}
