// migrate.go — the `tycswap migrate` pre-dispatched subcommand and the one-line
// hint every other command prints while it applies (DESIGN Amendment A23).
//
// migrate copies the store this fork came from into tycswap's own store, once,
// and never writes to the old one; internal/storemigrate does the work. It needs
// no switcher: constructing one would lay files down in the store it is about
// to fill.
package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/storemigrate"
)

const migrateProg = "tycswap migrate"

// runMigrate is the storemigrate seam, swapped by tests.
var runMigrate = storemigrate.Run

// migrateCommand handles `tycswap migrate [--dry-run] [--json]`. argv excludes
// "migrate".
func migrateCommand(_ string, argv []string, s ioStreams) int {
	var dryRun, jsonMode bool
	for _, tok := range argv {
		switch tok {
		case "--dry-run":
			dryRun = true
		case "--json":
			jsonMode = true
		case "-h", "--help":
			fmt.Fprintln(s.out, "usage: "+migrateProg+" [-h] [--dry-run] [--json]")
			fmt.Fprintln(s.out)
			fmt.Fprintln(s.out, "Copy the claude-swap store into tycswap's empty store, once.")
			fmt.Fprintln(s.out, "The old store and its Keychain items are only read, never changed.")
			return 0
		default:
			return subError(migrateProg, s.err, "unrecognized arguments: "+tok)
		}
	}
	if code, blocked := guardRoot(s.err); blocked {
		return code
	}
	setSigintJSON(jsonMode)

	rep, err := runMigrate(storemigrate.Options{DryRun: dryRun})
	if err != nil {
		var e error
		switch {
		case errors.Is(err, storemigrate.ErrNoOldStore):
			e = cerr.Migration("No old store to copy (looked in %s).", strings.Join(paths.OldBackupRoots(), ", "))
		case errors.Is(err, storemigrate.ErrNotEmpty):
			e = cerr.Migration("%s", err.Error())
		default:
			e = cerr.Migration("Copy from %s to %s failed: %v", rep.From, rep.To, err)
		}
		return renderDomainError(e, jsonMode, s.out, s.err)
	}

	if jsonMode {
		writeJSONIndent(s.out, migrateJSON(rep))
		return 0
	}
	printMigrateReport(s.out, rep)
	return 0
}

func migrateJSON(rep storemigrate.Report) map[string]any {
	dirs, files, links := rep.Counts()
	entries := rep.Entries
	if entries == nil {
		entries = []storemigrate.Entry{}
	}
	kc := rep.Keychain
	if kc == nil {
		kc = []storemigrate.KeychainItem{}
	}
	skipped := rep.Skipped
	if skipped == nil {
		skipped = []string{}
	}
	return map[string]any{
		"schemaVersion": jsonout.SchemaVersion,
		"from":          rep.From,
		"to":            rep.To,
		"dryRun":        rep.DryRun,
		"dirs":          dirs,
		"files":         files,
		"symlinks":      links,
		"entries":       entries,
		"skipped":       skipped,
		"keychain":      kc,
	}
}

// printMigrateReport prints one line per top-level store entry (with a file
// count for a directory), every rename, and every Keychain item.
func printMigrateReport(out io.Writer, rep storemigrate.Report) {
	verb := "Copied"
	if rep.DryRun {
		verb = "Would copy"
	}
	dirs, files, links := rep.Counts()
	fmt.Fprintf(out, "%s %s\n  to %s\n", printer.Accent(verb), rep.From, rep.To)
	fmt.Fprintf(out, "  %d directories, %d files, %d symlinks\n", dirs, files, links)

	perTop := map[string]int{}
	isDir := map[string]bool{}
	for _, e := range rep.Entries {
		parts := strings.SplitN(filepath.ToSlash(e.To), "/", 2)
		top := parts[0]
		if len(parts) == 1 {
			isDir[top] = e.Kind == "dir"
			continue
		}
		if e.Kind == "file" {
			perTop[top]++
		}
	}
	tops := make([]string, 0, len(isDir))
	for t := range isDir {
		tops = append(tops, t)
	}
	sort.Strings(tops)
	for _, t := range tops {
		if isDir[t] {
			fmt.Fprintf(out, "  %s/ %s\n", t, printer.Muted(fmt.Sprintf("(%d files)", perTop[t])))
		} else {
			fmt.Fprintf(out, "  %s\n", t)
		}
	}
	for _, e := range rep.Entries {
		if e.From != e.To {
			fmt.Fprintf(out, "  renamed %s -> %s\n", e.From, e.To)
		}
	}
	for _, k := range rep.Keychain {
		fmt.Fprintf(out, "  Keychain %s / %s -> %s\n", k.FromService, k.Account, k.ToService)
	}
	if rep.DryRun {
		fmt.Fprintln(out, printer.Dimmed("Dry run: nothing was written."))
		return
	}
	fmt.Fprintln(out, printer.Dimmed("The old store was only read; remove it yourself once nothing else uses it."))
}

// oldStoreHint is the seam the front controller calls for the migrate hint.
var oldStoreHint = func() string {
	return storemigrate.Hint(paths.GetBackupRoot(), paths.OldBackupRoots())
}

// printOldStoreHint writes the one-line migrate hint to stderr for every command
// but `migrate` itself, and never in --json mode (stderr stays clean for tools
// that capture both streams).
func printOldStoreHint(argv []string, stderr io.Writer) {
	if len(argv) > 0 && argv[0] == "migrate" {
		return
	}
	for _, tok := range argv {
		if tok == "--" {
			break
		}
		if tok == "--json" {
			return
		}
	}
	if h := oldStoreHint(); h != "" {
		fmt.Fprintln(stderr, printer.Dimmed(h))
	}
}
