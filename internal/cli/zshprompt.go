package cli

import (
	"bytes"
	_ "embed"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/termsafe"
)

//go:embed promptzsh/prompt.zsh
var flakelabPrompts string

//go:embed promptzsh/LICENSE
var flakelabPromptLicense string

var promptLookPath = exec.LookPath

const zshPromptAdapter = `
mode="$1"; question="$2"; initial="$3"; shift 3
case "$mode" in
  choose)
    prompt_choose "$question" "$@" || exit 130
    print -rn -- "$PROMPT_INDEX"
    ;;
  input)
    prompt_input "$question" "$initial" || exit 130
    print -rn -- "$PROMPT_TEXT"
    ;;
esac
`

func zshPromptScript(body string) string {
	return "# " + strings.ReplaceAll(flakelabPromptLicense, "\n", "\n# ") + "\n" + flakelabPrompts + "\n" + body
}

func askTerminal(s ioStreams, title string, choices []string, initial string) (int, string, bool) {
	zsh, err := promptLookPath("zsh")
	if err != nil {
		return askGoMenu(s, title, choices, initial)
	}
	mode := "input"
	if choices != nil {
		mode = "choose"
	}
	args := []string{"-f", "-c", zshPromptScript(zshPromptAdapter), "tycswap-prompt", mode, termsafe.Strip(title), termsafe.Strip(initial)}
	for _, choice := range choices {
		args = append(args, termsafe.Strip(choice))
	}
	var answer bytes.Buffer
	cmd := exec.Command(zsh, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, &answer, s.err
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 130 {
			fmt.Fprintf(s.err, "Terminal prompt failed: %v\n", err)
		}
		return 0, "", false
	}
	if choices == nil {
		return 0, termsafe.Strip(answer.String()), true
	}
	index, err := strconv.Atoi(answer.String())
	if err != nil || index < 1 || index > len(choices) {
		fmt.Fprintln(s.err, "Terminal prompt returned an invalid selection.")
		return 0, "", false
	}
	return index - 1, "", true
}

func showMenuCommand(s ioStreams, args []string) {
	words := append([]string{"tycswap"}, args...)
	if zsh, err := promptLookPath("zsh"); err == nil {
		cmd := exec.Command(zsh, append([]string{"-f", "-c", zshPromptScript("prompt_ran \"$@\""), "tycswap-prompt"}, words...)...)
		cmd.Stderr = s.err
		if cmd.Run() == nil {
			return
		}
	}
	for i, word := range words {
		word = termsafe.Strip(word)
		if strings.ContainsAny(word, " \t\"'`$\\;|&()<>*?[]{}!#~") || word == "" {
			word = posixQuote(word)
		}
		words[i] = word
	}
	fmt.Fprintln(s.err, "→ "+strings.Join(words, " "))
}
