package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestZshPromptFiltersAndCancelsWithoutStdout(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh is not installed")
	}
	for _, tc := range []struct {
		keys string
		ok   bool
	}{
		{"--LOGIN\n", true}, {"\x1b", false}, {"\x03", false},
	} {
		var stdout, stderr bytes.Buffer
		s := ioStreams{in: strings.NewReader(tc.keys), out: &stdout, err: &stderr}
		i, _, ok := askTerminal(s, "Add account", []string{"add  Save login", "add --login  Sign in"}, "")
		if ok != tc.ok || ok && i != 1 || stdout.Len() != 0 {
			t.Fatalf("selection=%d ok=%v stdout=%q stderr=%q", i, ok, stdout.String(), stderr.String())
		}
		answer := "cancelled"
		if ok {
			answer = "add --login  Sign in"
		}
		if !strings.Contains(stderr.String(), "Add account") || !strings.Contains(stderr.String(), "› "+answer) {
			t.Fatal("missing flakelab answered prompt: " + stderr.String())
		}
	}
}

func TestPromptLabelsAndCommandEchoNeverExecuteInput(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	value := "work $(touch '" + marker + "') `touch '" + marker + "'`"
	var stderr bytes.Buffer
	s := ioStreams{in: strings.NewReader("\n"), err: &stderr}
	if _, _, ok := askTerminal(s, value, []string{value}, ""); !ok {
		t.Fatal(stderr.String())
	}
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "zsh", true: "fallback"}[fallback], func(t *testing.T) {
			old := promptLookPath
			t.Cleanup(func() { promptLookPath = old })
			if fallback {
				promptLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
			}
			stderr.Reset()
			args := []string{"alias", "2", value, "", "a'b", "a\\b"}
			showMenuCommand(s, args)
			line := strings.TrimSuffix(strings.TrimPrefix(stderr.String(), "→ "), "\n")
			out, err := exec.Command(zsh, "-f", "-c", "set -- "+line+"; printf '%s\\0' \"$@\"").Output()
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			if want := append([]string{"tycswap"}, args...); !reflect.DeepEqual(got, want) {
				t.Fatalf("command does not round-trip: %v", got)
			}
		})
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("prompt executed user input: %v", err)
	}
}
