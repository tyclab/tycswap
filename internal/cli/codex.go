// codex.go — the `tycswap codex` pre-dispatched namespace: the Codex (ChatGPT)
// provider's command surface. Port of claude-swap PR #252 cli_codex.py.
//
// A namespace rather than a --provider flag: bare `tycswap switch` and `tycswap
// list` keep meaning Claude, so every script and every habit built on the
// existing CLI is untouched, and nothing here changes an existing command. It
// is pre-dispatched from run() alongside run/auto/config/map for the same
// reason they are: a positional verb cannot coexist with the main parser's
// mutually-exclusive flag group.
//
// Every verb except import-codex-auth first runs the one-time codex-auth
// import, because an explicit import the user has to discover means they run
// `tycswap codex list`, see nothing, and conclude tycswap is broken. Deliberate
// deviations from #252 (TYCSWAP plan): list/status --json carry schemaVersion
// and camelCase usage and errors under --json use the ErrorEnvelope (1);
// remove reports only a removal that happened (3); messages name the resolved
// slot number (4); import-codex-auth warns on an unsupported schema (5); the
// namespace accepts --debug (6). Export/import/purge run without the store
// lock, as the Python does and as the Claude transfer verbs do.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/procdetect"
	"github.com/tyclab/tycswap/internal/codex/registryimport"
	codexstore "github.com/tyclab/tycswap/internal/codex/store"
	codexswitcher "github.com/tyclab/tycswap/internal/codex/switcher"
	"github.com/tyclab/tycswap/internal/codex/transfer"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/termsafe"
)

const codexProg = "tycswap codex"

// newCodexSwitcher builds the Codex switcher for one command, with its prompts
// and warnings on the command's streams. A package var so tests inject an
// offline api client and a fixed running-process list; `tycswap auto` builds its
// Codex engine through it too.
var newCodexSwitcher = func(s ioStreams) *codexswitcher.Switcher {
	return codexswitcher.New(codexswitcher.Options{Stdout: s.out, Stdin: s.in})
}

// codexLookPath resolves the codex binary on PATH (shutil.which). A seam for
// the login tests.
var codexLookPath = exec.LookPath

// runCodexLogin runs the resolved codex binary directly, never through a
// shell: a shell function named `codex` is a common setup (one injecting
// --dangerously-bypass-approvals-and-sandbox is in the wild), and inheriting it
// would run the login under flags tycswap never chose. It returns the process's
// exit status.
var runCodexLogin = func(binary string, args []string, s ioStreams) (int, error) {
	cmd := exec.Command(binary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, s.out, s.err
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code > 0 {
			return code, nil
		}
		return 1, nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// codexVerb is one verb's grammar: its boolean and value flags (an alias such
// as -y maps to the same name as --yes), its required positional metavars, and
// how many optional positionals follow them.
type codexVerb struct {
	usage    string
	bools    []string
	values   []string
	required []string
	optional int
}

var codexVerbs = map[string]codexVerb{
	"list":              {usage: "list [-h] [--json] [--skip-api] [--token-status]", bools: []string{"--json", "--skip-api", "--token-status"}},
	"status":            {usage: "status [-h] [--json]", bools: []string{"--json"}},
	"switch":            {usage: "switch [-h] [--strategy {best}] [NUM|EMAIL|ALIAS]", values: []string{"--strategy"}, optional: 1},
	"add":               {usage: "add [-h] [--alias NAME]", values: []string{"--alias"}},
	"login":             {usage: "login [-h] [--device-auth] [--alias NAME]", bools: []string{"--device-auth"}, values: []string{"--alias"}},
	"remove":            {usage: "remove [-h] [-y] NUM|EMAIL|ALIAS", bools: []string{"--yes"}, required: []string{"NUM|EMAIL|ALIAS"}},
	"alias":             {usage: "alias [-h] [--unset] NUM|EMAIL [name]", bools: []string{"--unset"}, required: []string{"NUM|EMAIL"}, optional: 1},
	"disable":           {usage: "disable [-h] NUM|EMAIL|ALIAS", required: []string{"NUM|EMAIL|ALIAS"}},
	"enable":            {usage: "enable [-h] NUM|EMAIL|ALIAS", required: []string{"NUM|EMAIL|ALIAS"}},
	"swap":              {usage: "swap [-h] A B", required: []string{"A", "B"}},
	"move":              {usage: "move [-h] NUM|EMAIL|ALIAS SLOT", required: []string{"NUM|EMAIL|ALIAS", "SLOT"}},
	"export":            {usage: "export [-h] [--account NUM|EMAIL] PATH", values: []string{"--account"}, required: []string{"PATH"}},
	"import":            {usage: "import [-h] [--force] PATH", bools: []string{"--force"}, required: []string{"PATH"}},
	"purge":             {usage: "purge [-h] [-y]", bools: []string{"--yes"}},
	"import-codex-auth": {usage: "import-codex-auth [-h]"},
}

// codexVerbOrder is argparse's choice order, used in the invalid-choice error.
var codexVerbOrder = []string{"list", "status", "switch", "add", "login", "remove", "alias",
	"disable", "enable", "swap", "move", "export", "import", "purge", "import-codex-auth"}

// codexArgs is one parsed verb invocation.
type codexArgs struct {
	verb   string
	pos    []string
	bools  map[string]bool
	values map[string]string
	debug  bool
}

// codexCommand handles `tycswap codex ...`. argv excludes "codex".
func codexCommand(_ string, argv []string, s ioStreams) int {
	var debug bool
	i := 0
	for ; i < len(argv); i++ {
		tok := argv[i]
		switch {
		case tok == "--debug":
			debug = true
			continue
		case tok == "-h" || tok == "--help":
			renderCodexHelp(s.out)
			return 0
		case strings.HasPrefix(tok, "-"):
			return subError(codexProg, s.err, "unrecognized arguments: "+tok)
		}
		break
	}
	if i >= len(argv) {
		return subError(codexProg, s.err, "the following arguments are required: verb")
	}
	a, code, done := parseCodexVerb(argv[i], argv[i+1:], s)
	if done {
		return code
	}
	a.debug = a.debug || debug

	if a.debug {
		// The same console handler every other subcommand's --debug enables.
		log := logging.New(paths.GetBackupRoot(), true)
		api.Log, procdetect.Log, registryimport.Log = log, log, log
	}
	if c, blocked := guardRoot(s.err); blocked {
		return c
	}
	jsonMode := a.bools["--json"]
	setSigintJSON(jsonMode)
	setSigintNote("Operation cancelled")
	return runCodexVerb(a, jsonMode, s)
}

// parseCodexVerb applies one verb's grammar to its arguments. done reports an
// early exit (help, or a usage error already printed) with code.
func parseCodexVerb(verb string, argv []string, s ioStreams) (codexArgs, int, bool) {
	spec, ok := codexVerbs[verb]
	if !ok {
		quoted := make([]string, len(codexVerbOrder))
		for i, v := range codexVerbOrder {
			quoted[i] = "'" + v + "'"
		}
		return codexArgs{}, subError(codexProg, s.err, fmt.Sprintf(
			"argument verb: invalid choice: '%s' (choose from %s)", verb, strings.Join(quoted, ", "))), true
	}
	prog := codexProg + " " + verb
	a := codexArgs{verb: verb, bools: map[string]bool{}, values: map[string]string{}}
	isBool := func(f string) bool { return contains(spec.bools, f) }
	isValue := func(f string) bool { return contains(spec.values, f) }

	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		name, inline, hasInline := tok, "", false
		if strings.HasPrefix(tok, "--") {
			if eq := strings.IndexByte(tok, '='); eq > 0 {
				name, inline, hasInline = tok[:eq], tok[eq+1:], true
			}
		}
		if name == "-y" {
			name = "--yes"
		}
		switch {
		case tok == "-h" || tok == "--help":
			fmt.Fprintf(s.out, "usage: %s %s\n", codexProg, spec.usage)
			return a, 0, true
		case tok == "--debug":
			a.debug = true
		case isBool(name) && !hasInline:
			a.bools[name] = true
		case isValue(name):
			v := inline
			if !hasInline {
				if i+1 >= len(argv) {
					return a, subError(prog, s.err, "argument "+name+": expected one argument"), true
				}
				i++
				v = argv[i]
			}
			a.values[name] = v
		case strings.HasPrefix(tok, "-") && tok != "-":
			return a, subError(prog, s.err, "unrecognized arguments: "+tok), true
		default:
			a.pos = append(a.pos, tok)
		}
	}

	if len(a.pos) < len(spec.required) {
		return a, subError(prog, s.err, "the following arguments are required: "+
			strings.Join(spec.required[len(a.pos):], ", ")), true
	}
	if max := len(spec.required) + spec.optional; len(a.pos) > max {
		return a, subError(prog, s.err, "unrecognized arguments: "+strings.Join(a.pos[max:], " ")), true
	}
	if st, ok := a.values["--strategy"]; ok && st != "best" {
		return a, subError(prog, s.err, fmt.Sprintf(
			"argument --strategy: invalid choice: '%s' (choose from 'best')", st)), true
	}
	return a, 0, false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// runCodexVerb executes a parsed verb. Handled errors (every cerr value,
// including the alias ValidationError) render as the Claude verbs do: the
// ErrorEnvelope on stdout under --json, else a red "Error: <msg>" on stderr,
// exit 1.
func runCodexVerb(a codexArgs, jsonMode bool, s ioStreams) int {
	fail := func(err error) int { return renderDomainError(err, jsonMode, s.out, s.err) }

	if a.verb == "import-codex-auth" {
		sw := newCodexSwitcher(s)
		result, err := registryimport.Import(sw.Store(), registryimport.Options{})
		if err != nil {
			return fail(err)
		}
		// Deviation 5: the explicit import warns on a registry too new to read,
		// exactly as the automatic one does, instead of a silent "Imported 0".
		warnUnsupportedSchema(result, s.out)
		fmt.Fprintf(s.out, "Imported %d, skipped %d.\n", result.Imported, result.Skipped)
		return 0
	}

	sw := newCodexSwitcher(s)
	// Under --json the import notice goes to stderr so stdout stays exactly
	// one JSON document (DESIGN §3.2).
	notices := s.out
	if jsonMode {
		notices = s.err
	}
	if err := codexAutoImport(sw.Store(), notices); err != nil {
		return fail(err)
	}
	ctx := context.Background()

	switch a.verb {
	case "list":
		return codexList(ctx, sw, a.bools["--skip-api"], a.bools["--token-status"], jsonMode, s)
	case "status":
		st := sw.Status(ctx)
		if jsonMode {
			writeJSONIndent(s.out, st.JSON())
		} else {
			st.Render(s.out)
		}
		return 0
	case "switch":
		var res codexswitcher.SwitchResult
		var err error
		switch {
		case a.values["--strategy"] == "best":
			res, err = sw.SwitchBest(ctx)
		case len(a.pos) == 0:
			res, err = sw.Rotate(ctx)
		default:
			res, err = sw.SwitchTo(ctx, a.pos[0])
		}
		if err != nil {
			return fail(err)
		}
		if res.AlreadyActive {
			fmt.Fprintf(s.out, "Codex account %s is already active: %s\n", res.Number, termsafe.Strip(res.Email))
			return 0
		}
		fmt.Fprintf(s.out, "Switched to Codex account %s: %s\n", res.Number, termsafe.Strip(res.Email))
		if len(res.RunningPIDs) > 0 {
			warningTo(s.out, fmt.Sprintf("codex is running (pid %s) — restart it for the new account to take effect.",
				joinPIDs(res.RunningPIDs)))
		}
		return 0
	case "add":
		slot, err := sw.Add(ctx, a.values["--alias"])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(s.out, "Added Codex account %s: %s\n", slot.Number, termsafe.Strip(slot.DisplayLabel()))
		return 0
	case "login":
		return codexLogin(ctx, sw, a.bools["--device-auth"], a.values["--alias"], s)
	case "remove":
		number, _, _, err := sw.ResolveAccount(a.pos[0])
		if err != nil {
			return fail(err)
		}
		removed, err := sw.Remove(a.pos[0], a.bools["--yes"])
		if err != nil {
			return fail(err)
		}
		// Deviation 3: a declined prompt already printed "Cancelled".
		if removed {
			fmt.Fprintf(s.out, "Removed Codex account %s\n", number)
		}
		return 0
	case "alias":
		if a.bools["--unset"] || len(a.pos) < 2 || a.pos[1] == "" {
			number, err := sw.UnsetAlias(a.pos[0])
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(s.out, "Cleared alias for Codex account %s\n", number)
			return 0
		}
		number, alias, err := sw.Alias(a.pos[0], a.pos[1])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(s.out, "Codex account %s is now '%s'\n", number, alias)
		return 0
	case "disable", "enable":
		number, err := sw.SetAccountDisabled(a.pos[0], a.verb == "disable")
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(s.out, "Codex account %s %sd\n", number, a.verb)
		return 0
	case "swap":
		first, second, err := sw.Swap(a.pos[0], a.pos[1])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(s.out, "Swapped Codex slots %s and %s\n", first, second)
		return 0
	case "move":
		from, to, swapped, err := sw.Move(a.pos[0], a.pos[1])
		if err != nil {
			return fail(err)
		}
		switch {
		case from == to:
			fmt.Fprintf(s.out, "Codex account is already in slot %s\n", to)
		case swapped:
			fmt.Fprintf(s.out, "Moved Codex account %s to slot %s (swapped with its occupant)\n", from, to)
		default:
			fmt.Fprintf(s.out, "Moved Codex account %s to slot %s\n", from, to)
		}
		return 0
	case "export":
		path := a.pos[0]
		count, err := transfer.Export(sw.Store(), path, a.values["--account"], s.out)
		if err != nil {
			return fail(err)
		}
		if path != "-" {
			fmt.Fprintf(s.out, "Exported %d Codex account(s) to %s\n", count, path)
		}
		return 0
	case "import":
		count, err := transfer.Import(sw.Store(), a.pos[0], a.bools["--force"], s.in)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(s.out, "Imported %d Codex account(s)\n", count)
		return 0
	case "purge":
		if _, err := transfer.Purge(sw.Store(), a.bools["--yes"], s.in, s.out); err != nil {
			return fail(err)
		}
		return 0
	}
	return 0
}

// codexAutoImport imports codex-auth's accounts the first time, once
// (cli_codex.py _auto_import). The source tree is left untouched, so this is
// reversible.
func codexAutoImport(st *codexstore.Store, out io.Writer) error {
	result, err := registryimport.Import(st, registryimport.Options{OnlyIfEmpty: true})
	if err != nil {
		return err
	}
	if warnUnsupportedSchema(result, out) {
		return nil
	}
	if result.Imported > 0 {
		fmt.Fprintln(out, printer.Dimmed(fmt.Sprintf(
			"Imported %d Codex account(s) from %s (the original is left untouched).", result.Imported, result.Source)))
	}
	if result.Skipped > 0 {
		fmt.Fprintln(out, printer.Dimmed(fmt.Sprintf("Skipped %d account(s) with no usable auth file.", result.Skipped)))
	}
	return nil
}

// warnUnsupportedSchema prints the schema-too-new warning and reports whether
// it did.
func warnUnsupportedSchema(result registryimport.Result, out io.Writer) bool {
	if result.UnsupportedSchema == nil {
		return false
	}
	warningTo(out, fmt.Sprintf("codex-auth registry uses schema %d, which this version of tycswap does not understand — not importing.",
		*result.UnsupportedSchema))
	return true
}

// codexList prints the roster (cli_codex.py _print_list). skipAPI is an empty
// fetch set (no network at all); otherwise every account is eligible.
func codexList(ctx context.Context, sw *codexswitcher.Switcher, skipAPI, tokenStatus, jsonMode bool, s ioStreams) int {
	var fetch map[string]bool
	if skipAPI {
		fetch = map[string]bool{}
	}
	snap := sw.AccountsSnapshot(ctx, fetch)

	if jsonMode {
		rows := make([]map[string]any, 0, len(snap.Accounts))
		for _, a := range snap.Accounts {
			rows = append(rows, codexRowJSON(a))
		}
		var active any
		if snap.ActiveNumber != "" {
			active = snap.ActiveNumber
		}
		payload := map[string]any{
			"schemaVersion": jsonout.SchemaVersion,
			"provider":      reporting.ProviderCodex,
			"activeNumber":  active,
			"accounts":      rows,
		}
		if tokenStatus {
			statuses := make([]map[string]any, 0, len(snap.Accounts))
			for _, a := range snap.Accounts {
				st, err := sw.TokenStatus(a.Number)
				if err != nil {
					return renderDomainError(err, true, s.out, s.err)
				}
				statuses = append(statuses, st)
			}
			payload["tokenStatus"] = statuses
		}
		writeJSONIndent(s.out, payload)
		return 0
	}

	if len(snap.Accounts) == 0 {
		fmt.Fprintln(s.out, "No Codex accounts. Run 'tycswap codex add' or 'tycswap codex login'.")
		return 0
	}
	for _, a := range snap.Accounts {
		marker := " "
		if a.IsActive {
			marker = "*"
		}
		// Display fields come from the Codex API and other tools' files: the
		// JSON row carries them as stored, the text row never prints a terminal
		// control sequence they carry.
		alias := ""
		if a.Alias != "" {
			alias = " (" + termsafe.Strip(a.Alias) + ")"
		}
		state := ""
		if a.Disabled {
			state = " [disabled]"
		}
		summary := a.Usage.Sentinel
		if summary == "" {
			summary = codexUsageSummary(a.Usage.LastGood)
		}
		suffix := ""
		if summary != "" {
			suffix = "  " + summary
		}
		fmt.Fprintf(s.out, "%s %s. %s [%s]%s%s%s\n", marker, a.Number, termsafe.Strip(a.Email), termsafe.Strip(a.DisplayTag()), alias, state, suffix)

		if tokenStatus {
			st, err := sw.TokenStatus(a.Number)
			if err != nil {
				return renderDomainError(err, false, s.out, s.err)
			}
			fmt.Fprintln(s.out, printer.Dimmed(codexTokenLine(st)))
		}
	}
	return 0
}

// codexRowJSON is one `list --json` row. Deviation 1: usage is projected
// through jsonout.UsageToJSON (fiveHour/sevenDay/spend), never the raw
// snake_case dict; the #252 field names are otherwise kept.
func codexRowJSON(a reporting.AccountSnapshot) map[string]any {
	var usageVal, plan, sentinel any
	if a.Usage.LastGood != nil {
		usageVal = jsonout.UsageToJSON(a.Usage.LastGood)
		plan = a.Usage.LastGood["plan"]
	}
	if a.Usage.Sentinel != "" {
		sentinel = a.Usage.Sentinel
	}
	var fetchedAt, age any
	if a.Usage.FetchedAt != nil {
		fetchedAt = *a.Usage.FetchedAt
	}
	if a.Usage.AgeS != nil {
		age = *a.Usage.AgeS
	}
	return map[string]any{
		"number":     a.Number,
		"email":      a.Email,
		"workspace":  a.OrgName,
		"alias":      a.Alias,
		"active":     a.IsActive,
		"disabled":   a.Disabled,
		"kind":       a.Kind,
		"usage":      usageVal,
		"sentinel":   sentinel,
		"fetchedAt":  fetchedAt,
		"ageSeconds": age,
		"plan":       plan,
	}
}

// codexTokenLine is the dimmed --token-status line under a roster row. It
// never includes token material, only facts derived from it.
func codexTokenLine(st map[string]any) string {
	state, _ := st["state"].(string)
	if state != "oauth" {
		return "      token: " + state
	}
	due := "valid"
	if b, _ := st["refreshDue"].(bool); b {
		due = "refresh due"
	}
	rt := ""
	if b, _ := st["hasRefreshToken"].(bool); !b {
		rt = ", NO refresh token"
	}
	last := "never"
	if v, ok := st["lastRefresh"].(string); ok && v != "" {
		last = v
	}
	var in *float64
	if f, ok := codexFloat(st["expiresInSeconds"]); ok {
		in = &f
	}
	return fmt.Sprintf("      token: %s, expires in %s%s; last refresh %s", due, codexRelative(in), rt, last)
}

// codexUsageSummary renders "5h N%  7d N%" from a usage dict.
func codexUsageSummary(u map[string]any) string {
	var parts []string
	for _, w := range []struct{ key, label string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		window, _ := u[w.key].(map[string]any)
		if pct, ok := codexFloat(window["pct"]); ok {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", w.label, pct))
		}
	}
	return strings.Join(parts, "  ")
}

// codexRelative is a compact human duration: "6d 4h", "3h 12m", "12m",
// "expired", or "unknown".
func codexRelative(seconds *float64) string {
	if seconds == nil {
		return "unknown"
	}
	if *seconds <= 0 {
		return "expired"
	}
	total := int(*seconds)
	days, rem := total/86400, total%86400
	hours, rem := rem/3600, rem%3600
	minutes := rem / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// codexFloat accepts every numeric form a usage or token-status value takes
// (values read back from the usage store are json.Number).
func codexFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func joinPIDs(pids []int) string {
	out := make([]string, len(pids))
	for i, p := range pids {
		out[i] = strconv.Itoa(p)
	}
	return strings.Join(out, ", ")
}

// codexLogin runs `codex login`, then captures the account it produced
// (cli_codex.py _do_login). A failed login exits with codex's own status and
// stores nothing.
func codexLogin(ctx context.Context, sw *codexswitcher.Switcher, deviceAuth bool, alias string, s ioStreams) int {
	binary, err := codexLookPath("codex")
	if err != nil || binary == "" {
		errorTo(s.err, "The 'codex' CLI is not on PATH. Install it, or run 'tycswap codex add' after logging in another way.")
		return 1
	}
	if abs, aerr := filepath.Abs(binary); aerr == nil {
		binary = abs
	}
	args := []string{"login"}
	if deviceAuth {
		args = append(args, "--device-auth")
	}
	// codex login overwrites ~/.codex/auth.json. Store the outgoing account's
	// newest tokens first: codex rotates refresh tokens in place, so the copy
	// in its snapshot may already be dead.
	if err := sw.CaptureLive(); err != nil {
		errorTo(s.err, "Could not save the current Codex login before 'codex login' replaces it: "+err.Error())
		return 1
	}
	code, err := runCodexLogin(binary, args, s)
	if err != nil || code != 0 {
		errorTo(s.err, "codex login did not complete.")
		if code == 0 {
			code = 1
		}
		return code
	}
	slot, err := sw.Add(ctx, alias)
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	fmt.Fprintf(s.out, "Added Codex account %s: %s\n", slot.Number, termsafe.Strip(slot.DisplayLabel()))
	return 0
}

// renderCodexHelp writes `tycswap codex --help` (cli_codex.py's parser
// description and RawDescription epilog, verbatim apart from --debug).
func renderCodexHelp(out io.Writer) {
	fmt.Fprintln(out, "usage: tycswap codex [-h] [--debug] {"+strings.Join(codexVerbOrder, ",")+"} ...")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Multi-account switcher for the Codex CLI")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "options:")
	fmt.Fprintln(out, "  -h, --help            show this help message and exit")
	fmt.Fprintln(out, "  --debug               Enable debug logging")
	fmt.Fprint(out, codexEpilog)
}

const codexEpilog = `
Commands:
  list [--json] [--skip-api] [--token-status]
                          list managed Codex accounts and their usage
  status [--json]         show the account the codex CLI is currently using
  switch                  rotate to the next account
  switch <num|email|alias>
                          activate a specific account
  switch --strategy best  jump to the account with the most quota left
  add [--alias NAME]      store the account you are currently logged in as
  login [--device-auth] [--alias NAME]
                          run 'codex login', then store the result
  remove <num|email> [-y] forget an account and delete its credentials
  alias <num|email> NAME  set a short alias  (--unset to clear)
  disable|enable <target> hold an account out of / return it to auto-rotation
  swap <a> <b>            exchange two accounts' slot numbers
  move <target> <slot>    assign an account to a slot (swaps if taken)
  export <path>           export accounts (with tokens) to a file; '-' = stdout
  import <path>           import accounts from a file; '-' = stdin
  purge                   remove all tycswap Codex data (your login is untouched)
  import-codex-auth       re-run the one-time codex-auth registry import

Examples:
  tycswap codex list                    5h / weekly usage per account
  tycswap codex list --skip-api         cached only, no network
  tycswap codex list --token-status     token expiry (never prints the token)
  tycswap codex list --json             machine-readable
  tycswap codex switch                  rotate to the next account
  tycswap codex switch work             by alias
  tycswap codex switch --strategy best  most quota left
  tycswap codex status --json           machine-readable status
  tycswap codex disable 2               keep it out of ` + "`tycswap auto`" + ` rotation
  tycswap codex swap 1 2                exchange slot numbers
  tycswap codex export ~/codex.json     back up accounts (contains live tokens)

Auto-switching:
  Codex rides along in ` + "`tycswap auto`" + `, which rotates both providers. Tune it with
  ` + "`tycswap config set autoswitch.codexThreshold 85`" + ` (0 = inherit
  autoswitch.sevenDayThreshold) or turn it off with ` + "`autoswitch.codexEnabled false`" + `.
  A Codex API-key login is never a rotation target, as on the Claude side: it
  reports no usage, so a threshold has nothing to compare.

Notes:
  Bare ` + "`tycswap list` / `tycswap switch`" + ` still mean Claude — nothing you already
  type changes.

  Switching rewrites ~/.codex/auth.json. A codex session that is ALREADY
  RUNNING keeps its old account until you restart it; tycswap warns you and names
  the running PIDs. This applies to automatic switching too — it only affects
  the next session you start.

  If you use codex-auth, your accounts are imported automatically on the first
  ` + "`tycswap codex`" + ` command. ~/.codex/accounts/ is left untouched.
`
