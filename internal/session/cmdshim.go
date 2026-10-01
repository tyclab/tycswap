package session

import (
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
)

// cmdMetachars are the characters cmd.exe acts on when it re-parses the
// command line of a .cmd or .bat file, plus the line breaks that end it.
const cmdMetachars = "&|<>^%!\"()\r\n"

// CheckCmdShimArgs refuses arguments that cmd.exe would interpret when bin is
// a .cmd or .bat file (an npm shim for claude on Windows). Go starts such a
// file through cmd.exe, which re-parses the whole command line, so a `&` or
// `|` in an argument passed through `tycswap run -- …` would run a second
// command. Any other target is started directly and needs no check.
func CheckCmdShimArgs(bin string, args []string) error {
	ext := strings.ToLower(filepath.Ext(bin))
	if ext != ".cmd" && ext != ".bat" {
		return nil
	}
	for _, a := range args {
		if i := strings.IndexAny(a, cmdMetachars); i >= 0 {
			return cerr.Validation("refusing to pass %q to %s: %s scripts are run through cmd.exe, which would interpret %q; "+
				"drop the character, or install claude's native executable", a, filepath.Base(bin), ext, a[i:i+1])
		}
	}
	return nil
}
