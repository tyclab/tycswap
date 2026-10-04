// Command gen writes the tray icons appicon embeds (rendered/*.png). Run it
// through `go generate ./internal/appicon` after changing the drawing code;
// the package's tests fail until the files match the code again.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/tyclab/tycswap/internal/appicon"
)

func main() {
	dir := "rendered"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	files := appicon.Renderings()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
