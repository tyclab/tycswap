package session

import (
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
)

// cmdMetachars are the characters cmd.exe acts on when it re-parses the
// command line of a .cmd or .bat file, plus the line breaks that end it.
const cmdMetachars = "&|<>^%!\"()\r\n"

// Go starts a .cmd/.bat through cmd.exe, which re-parses the line: a & or | in a `run --` argument would run a second command.
func CheckCmdShimArgs(bin string, args []string) error {
	if !isCmdShim(bin) {
		return nil
	}
	for _, a := range args {
		if i := strings.IndexAny(a, cmdMetachars); i >= 0 {
			return cerr.Validation("refusing to pass %q to %s: %s scripts are run through cmd.exe, which would interpret %q; "+
				"drop the character, or install claude's native executable", a, filepath.Base(bin), strings.ToLower(filepath.Ext(bin)), a[i:i+1])
		}
	}
	return nil
}

// isCmdShim reports whether bin is a .cmd or .bat file.
func isCmdShim(bin string) bool {
	ext := strings.ToLower(filepath.Ext(bin))
	return ext == ".cmd" || ext == ".bat"
}
