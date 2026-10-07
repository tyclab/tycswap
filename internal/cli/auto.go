// auto.go — the `tycswap auto` pre-dispatched subcommand (spec 08§7.7, 05§19).
//
// Implements spec 08§7.7 / 05§19: the flag grammar (--once/--json/--interval/
// --five-hour-threshold/--seven-day-threshold/--model-threshold/--cooldown/
// --model/--dry-run/--debug; one threshold flag per window replaces
// --threshold, DESIGN A34, and the include-api-key-accounts pair is gone with
// its setting, DESIGN A33), merged_with_cli, the engine construction, --once
// (exit = outcome), loop mode with SIGTERM→Stop and the dimmed banner, and the
// JSONL/human emit callbacks. The compact JSONL/error-envelope discipline
// (spec 08§7.7) is distinct from the main path's indent-2. prog is hardcoded
// "tycswap auto".
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	codexauto "github.com/tyclab/tycswap/internal/codex/autoswitch"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/settings"
)

const autoProg = "tycswap auto"

// autoCommand handles `tycswap auto ...` (spec 08§7.7). argv excludes "auto".
func autoCommand(_ string, argv []string, s ioStreams) int {
	var once, jsonMode, dryRun, debug bool
	var interval, cooldown *float64
	// One flag per bar, named after the window it governs (DESIGN A34).
	var fiveHour, sevenDay, modelBar *float64
	var model *string

	// takeFloat / takeStr consume the value for a value flag, erroring (exit 2)
	// on a missing/invalid argument.
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		next := func() (string, bool) {
			if i+1 < len(argv) {
				i++
				return argv[i], true
			}
			return "", false
		}
		switch {
		case tok == "--once":
			once = true
		case tok == "--json":
			jsonMode = true
		case tok == "--dry-run":
			dryRun = true
		case tok == "--debug":
			debug = true
		case tok == "--interval":
			v, ok := next()
			if !ok {
				return subError(autoProg, s.err, "argument --interval: expected one argument")
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return subError(autoProg, s.err, fmt.Sprintf("argument --interval: invalid float value: '%s'", v))
			}
			interval = &f
		case tok == "--five-hour-threshold" || tok == "--seven-day-threshold" || tok == "--model-threshold":
			v, ok := next()
			if !ok {
				return subError(autoProg, s.err, "argument "+tok+": expected one argument")
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return subError(autoProg, s.err, fmt.Sprintf("argument %s: invalid float value: '%s'", tok, v))
			}
			switch tok {
			case "--five-hour-threshold":
				fiveHour = &f
			case "--seven-day-threshold":
				sevenDay = &f
			default:
				modelBar = &f
			}
		case tok == "--cooldown":
			v, ok := next()
			if !ok {
				return subError(autoProg, s.err, "argument --cooldown: expected one argument")
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return subError(autoProg, s.err, fmt.Sprintf("argument --cooldown: invalid float value: '%s'", v))
			}
			cooldown = &f
		case tok == "--model":
			v, ok := next()
			if !ok {
				return subError(autoProg, s.err, "argument --model: expected one argument")
			}
			model = &v
		case tok == "-h" || tok == "--help":
			renderAutoHelp(s.out)
			return 0
		default:
			return subError(autoProg, s.err, "unrecognized arguments: "+tok)
		}
	}

	sw, err := constructSwitcher(debug, s.err)
	if err != nil {
		return autoError(err, jsonMode, s)
	}
	if code, blocked := guardRoot(s.err); blocked {
		return code
	}
	setSigintJSON(jsonMode)
	setSigintNote("Auto-switch stopped")

	merged := settings.MergedWithCLI(settings.Load(sw.BackupDir()), settings.CLIOverrides{
		FiveHourThreshold: fiveHour,
		SevenDayThreshold: sevenDay,
		ModelThreshold:    modelBar,
		IntervalSeconds:   interval,
		CooldownSeconds:   cooldown,
		Model:             model,
	})

	onEvent := humanEmit(s.out)
	if jsonMode {
		onEvent = jsonlEmit(s.out)
	}
	// The Codex engine emits from its own goroutine in loop mode; one mutex
	// keeps the two engines' lines whole on the shared stdout.
	var outMu sync.Mutex
	emitClaude := onEvent
	onEvent = func(ev autoswitch.Event) {
		outMu.Lock()
		defer outMu.Unlock()
		emitClaude(ev)
	}
	engine := autoswitch.NewEngine(autoswitchAdapter{sw}, merged, onEvent, dryRun,
		autoswitch.WithOAuthClient(sw.OAuth),
		autoswitch.WithLogger(sw.Log),
		autoswitch.WithClock(sw.Clk),
	)

	// Codex rides along in this process as its own small engine (claude-swap
	// PR #252 cli.py _codex_auto_engine); nil on a Claude-only machine, which
	// then behaves exactly as before.
	codexEngine := newCodexAutoEngine(merged, s)
	runCodexTick := func(ctx context.Context) {
		if codexEngine == nil {
			return
		}
		tick := codexEngine.Tick(ctx, dryRun)
		if !codexTickShown(tick, dryRun) {
			return
		}
		outMu.Lock()
		defer outMu.Unlock()
		emitCodexTick(s.out, tick, jsonMode)
	}

	if once {
		// Claude first, then Codex; the exit status is the Claude outcome.
		code := int(engine.Tick())
		tickGroupScopes(sw, merged, onEvent, dryRun)
		runCodexTick(context.Background())
		return code
	}

	// Loop mode: SIGTERM (systemd stop) exits the loop cleanly (spec 05§19).
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	go func() {
		<-sigterm
		engine.Stop()
	}()
	if !jsonMode {
		dry := ""
		if dryRun {
			dry = " (dry-run)"
		}
		fmt.Fprintln(s.out, printer.Dimmed(fmt.Sprintf(
			"Auto-switch running: 5h %s%% · 7d %s%% · model %s%%, every %.0fs%s — Ctrl-C to stop",
			pctText(merged.FiveHourThreshold), pctText(merged.SevenDayThreshold), pctText(merged.ModelThreshold),
			merged.IntervalSeconds, dry)))
	}
	stopCodex := startCodexLoop(true,
		time.Duration(merged.IntervalSeconds*float64(time.Second)), func(ctx context.Context) {
			tickGroupScopes(sw, merged, onEvent, dryRun)
			runCodexTick(ctx)
		})
	code := engine.RunLoop()
	stopCodex()
	return code
}

// emitCodexTick prints one Codex tick in the engine's event contract
// (deviation 2): a compact JSONL object with schemaVersion/event/ts under
// --json, else the same timestamped, kind-colored line the Claude events use.
func emitCodexTick(out io.Writer, tick codexauto.Tick, jsonMode bool) {
	now := time.Now()
	if jsonMode {
		line := codexTickFields(tick)
		line["schemaVersion"] = jsonout.SchemaVersion
		line["event"] = "codex"
		line["ts"] = now.UTC().Format("2006-01-02T15:04:05Z")
		writeJSONCompact(out, line)
		return
	}
	line := tick.Human()
	switch tick.Outcome {
	case codexauto.OutcomeSwitched:
		line = printer.Accent(line)
	case codexauto.OutcomeError:
		line = printer.Yellowed(line)
	default:
		line = printer.Dimmed(line)
	}
	fmt.Fprintf(out, "%s  %s\n", now.Format("15:04:05"), line)
}

// jsonlEmit prints one compact JSON object per event on stdout (spec 08§7.7).
func jsonlEmit(out io.Writer) func(autoswitch.Event) {
	return func(ev autoswitch.Event) { writeJSONCompact(out, ev.JSON()) }
}

// humanEmit prints a timestamped, kind-colored human line per event (spec
// 08§7.7 human_emit / 05§19).
func humanEmit(out io.Writer) func(autoswitch.Event) {
	return func(ev autoswitch.Event) {
		stamp := time.Now().Format("15:04:05")
		line := ev.Human()
		switch ev.Kind() {
		case "switch":
			line = printer.Accent(line)
		case "error", "account-quarantined":
			line = printer.Yellowed(line)
		case "poll", "no-switch", "sleep":
			line = printer.Dimmed(line)
		}
		fmt.Fprintf(out, "%s  %s\n", stamp, line)
	}
}

// autoError presents a ClaudeSwitchError: the COMPACT JSON envelope in JSON
// mode, else a red stderr line; exit 1 (spec 08§7.7).
func autoError(err error, jsonMode bool, s ioStreams) int {
	if jsonMode {
		writeJSONCompact(s.out, jsonout.ErrorEnvelope(err))
	} else {
		errorTo(s.err, "Error: "+err.Error())
	}
	return 1
}

func renderAutoHelp(out io.Writer) {
	fmt.Fprintln(out, "usage: tycswap auto [-h] [--once] [--json] [--interval SECONDS]")
	fmt.Fprintln(out, "                  [--five-hour-threshold PCT] [--seven-day-threshold PCT]")
	fmt.Fprintln(out, "                  [--model-threshold PCT] [--cooldown SECONDS] [--model NAMES]")
	fmt.Fprintln(out, "                  [--dry-run] [--debug]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Each window has a bar of its own: the 5h window bursts and refills in hours,")
	fmt.Fprintln(out, "the 7d one is the account's whole budget, and a per-model week counts only")
	fmt.Fprintln(out, "with --model.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Auto-switch rotates between subscription accounts only: it never moves onto")
	fmt.Fprintln(out, "or off an API-key account, since that changes how Claude Code authenticates.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Exit codes with --once:")
	fmt.Fprintln(out, "  0  switched to another account")
	fmt.Fprintln(out, "  1  error (network trouble, lock contention, ...)")
	fmt.Fprintln(out, "  2  no action needed")
	fmt.Fprintln(out, "  3  blocked: wanted to switch but no viable target / all exhausted")
}

// pctText renders a bar the way the settings show it: 85 as "85", 99.5 as
// "99.5".
func pctText(v float64) string { return settings.FormatSettingValue(v) }
