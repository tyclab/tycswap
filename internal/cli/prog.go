// prog.go — program-name derivation for usage/help (spec 08§1 _prog_name).
//
// Implements spec 08§1 (_prog_name) and 10-audit Gap 2 (the __main__→"tycswap"
// fallback). A Go binary has no importlib metadata; the name shown in usage is
// the invoked basename (stripping a trailing .exe), falling back to "tycswap".
package cli

import (
	"path/filepath"
	"strings"
)

func progName(argv0 string) string {
	name := filepath.Base(argv0)
	if name == "." || name == string(filepath.Separator) {
		name = ""
	}
	for _, ext := range []string{".exe", ".pyw", ".py"} {
		if len(name) >= len(ext) && strings.EqualFold(name[len(name)-len(ext):], ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	switch name {
	case "", "__main__", "python", "python3", "py":
		return "tycswap"
	}
	return name
}
