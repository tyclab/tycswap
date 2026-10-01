package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestHelpFlag pins the --help structure (spec 08§14): the header, bare
// subcommands, the visible flags, the "keep working" note, and the absence of
// legacy flag spellings / argparse's raw "one of the arguments" from the
// options section (the substring before "Flags combine with subcommands:").
func TestHelpFlag(t *testing.T) {
	var out bytes.Buffer
	if code := renderMainHelp("tycswap", &out); code != 0 {
		t.Fatalf("renderMainHelp code = %d, want 0", code)
	}
	help := out.String()

	mustContain := []string{
		"Multi-Account Switcher for Claude Code",
		"tycswap switch <num|email>",
		"tycswap list",
		"tycswap status",
		"tycswap add",
		"tycswap add-token [TOKEN|-]",
		"tycswap export <path>",
		"tycswap import <path>",
		"tycswap upgrade",
		"tycswap alias <num|email>",
		"tycswap auto",
		"tycswap config",
		"--slot",
		"--email",
		"tycswap run 2 -- --resume", // epilog example
		"keep working",
	}
	for _, s := range mustContain {
		if !strings.Contains(help, s) {
			t.Errorf("--help missing %q", s)
		}
	}

	// The legacy --flag spellings must be absent from the options section
	// (everything before the epilog marker), and argparse's raw error text too.
	marker := "Flags combine with subcommands:"
	idx := strings.Index(help, marker)
	if idx < 0 {
		t.Fatalf("--help missing epilog marker %q", marker)
	}
	optionsSection := help[:idx]
	for _, legacy := range []string{"--add-account", "--switch-to", "one of the arguments"} {
		if strings.Contains(optionsSection, legacy) {
			t.Errorf("options section must not contain %q", legacy)
		}
	}
}

// TestHelpProgSubstitution: a non-default program name is substituted throughout
// (%(prog)s parity), including the purge line.
func TestHelpProgSubstitution(t *testing.T) {
	var out bytes.Buffer
	renderMainHelp("claude-swap", &out)
	help := out.String()
	if !strings.Contains(help, "claude-swap list") {
		t.Errorf("--help did not substitute prog into command lines: %q", firstLines(help, 6))
	}
	if !strings.Contains(help, "remove all claude-swap data") {
		t.Errorf("--help did not substitute prog into the purge line")
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
