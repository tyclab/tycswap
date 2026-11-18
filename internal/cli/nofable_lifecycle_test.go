// The no-change rule for the account lifecycle (DESIGN A101): with no Fable
// login, add, remove, move, swap, purge, export and import create no Fable
// scope and print what they printed before.
package cli

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/transfer"
)

type yesPrompter struct{}

func (yesPrompter) Prompt(string) (string, bool) { return "y", true }
func (yesPrompter) Secret(string) (string, bool) { return "", false }
func (yesPrompter) StdinLine() (string, bool)    { return "", false }

var lifecycleVolatile = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z?|login\.[A-Za-z0-9]+`)

// runAll runs argv with every writer a lifecycle command prints to captured,
// in the order written.
func runAll(t *testing.T, argv ...string) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevOut, prevLife, prevTOut, prevTErr := os.Stdout, lifecycle.Output, transfer.Stdout, transfer.Stderr
	os.Stdout, lifecycle.Output, transfer.Stdout, transfer.Stderr = w, w, w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	var code int
	func() {
		defer func() {
			os.Stdout, lifecycle.Output, transfer.Stdout, transfer.Stderr = prevOut, prevLife, prevTOut, prevTErr
		}()
		code = run("tycswap", argv, ioStreams{in: strings.NewReader(""), out: w, err: w}, false, false)
	}()
	w.Close()
	return code, <-done
}

func TestNoFableLoginLifecycleNoChange(t *testing.T) {
	f := newLoginFixture(t)
	prev := lifecycle.ActivePrompter
	lifecycle.ActivePrompter = yesPrompter{}
	t.Cleanup(func() { lifecycle.ActivePrompter = prev })
	envelope := filepath.Join(f.home, "accounts.tycswap")
	root := paths.GetBackupRoot()

	var b strings.Builder
	for _, argv := range [][]string{
		{"add", "--login"},
		{"add"},
		{"move", "2", "5"},
		{"swap", "1", "5"},
		{"export", envelope},
		{"import", envelope, "--force"},
		{"remove", "5"},
		{"purge"},
	} {
		code, out := runAll(t, argv...)
		for _, dir := range []string{filepath.Join(root, "fable"), filepath.Join(root, "credentials-fable")} {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("%v created %s (%v)", argv, dir, err)
			}
		}
		entry := "=== " + strings.Join(argv, " ") + " | exit " + strconv.Itoa(code) + "\n" + out
		b.WriteString(lifecycleVolatile.ReplaceAllString(strings.ReplaceAll(entry, f.home, "<HOME>"), "<T>"))
	}
	if record := os.Getenv("TYCSWAP_NOFABLE_LIFECYCLE_RECORD"); record != "" {
		if err := os.WriteFile(filepath.Clean(record), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(b.String(), "=== purge | exit 0\n") {
		t.Errorf("the sequence did not run through:\n%s", b.String())
	}
}
