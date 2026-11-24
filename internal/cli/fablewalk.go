// fablewalk.go — `tycswap fable --login`: the Fable sign-in of every account
// that still lacks a usable Fable login, one after another (DESIGN A114).
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/termsafe"
)

const (
	fableWalkNone   = "Every account that runs Fable has a Fable login."
	fableWalkAgain  = "Run tycswap fable --login again for the rest."
	fableWalkPrompt = "Enter to open the browser, s to skip, q to stop: "
)

// fableWalkSignIn is one account's sign-in: `tycswap add --login --lineage
// fable --slot N` itself, its validation, locks and log lines included.
func fableWalkSignIn(prog, num string, s ioStreams) int {
	code, _ := addCommand(prog, []string{"--login", "--lineage", "fable", "--slot", num}, s)
	return code
}

// fableLoginWalk asks, in roster order, for each account fable --list marks
// Missing and runs its sign-in on Enter, never two at once, then sums up. A
// Ctrl-C ends the process after the running sign-in's own cleanup (sigint.go).
func fableLoginWalk(prog string, sw *core.Switcher, s ioStreams, terminal bool) int {
	if _, err := sw.Store.ForFable(); err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	rows, err := fableRows(sw)
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	var missing []fableRow
	for _, r := range rows {
		if r.Missing {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		fmt.Fprintln(s.out, fableWalkNone)
		return 0
	}
	if !terminal {
		err := cerr.Validation("%s fable --login needs a terminal on stdin and stdout: it asks before each browser sign-in", prog)
		return renderDomainError(err, false, s.out, s.err)
	}
	setSigintNote("Fable sign-ins stopped. " + fableWalkAgain)
	var signedIn, skipped, failed []string
	for i, r := range missing {
		name := termsafe.Strip(r.Email) + " (Account-" + r.Number + ")"
		if i > 0 {
			fmt.Fprintln(s.out)
		}
		fmt.Fprintf(s.out, "Fable sign-in %d of %d: %s.\n", i+1, len(missing), name)
		answer := askFableWalk(s)
		if answer == "q" {
			for _, rest := range missing[i:] {
				skipped = append(skipped, termsafe.Strip(rest.Email)+" (Account-"+rest.Number+")")
			}
			break
		}
		if answer == "s" {
			skipped = append(skipped, name)
			continue
		}
		errs := &signInErrors{Writer: s.err}
		if code := fableWalkSignIn(prog, r.Number, ioStreams{in: s.in, out: s.out, err: errs}); code != 0 {
			failed = append(failed, name+": "+errs.reason(code))
		} else {
			signedIn = append(signedIn, name)
		}
	}
	fmt.Fprintln(s.out)
	if len(signedIn) > 0 {
		fmt.Fprintln(s.out, "Signed in for Fable: "+strings.Join(signedIn, ", ")+".")
	}
	if len(skipped) > 0 {
		fmt.Fprintln(s.out, "Skipped: "+strings.Join(skipped, ", ")+".")
	}
	for _, f := range failed {
		fmt.Fprintln(s.out, "Failed: "+f)
	}
	if len(skipped)+len(failed) > 0 {
		fmt.Fprintln(s.out, fableWalkAgain)
	}
	if len(failed) > 0 {
		return 1
	}
	return 0
}

// askFableWalk asks until the answer is Enter (""), s or q; the end of input
// is q.
func askFableWalk(s ioStreams) string {
	for {
		fmt.Fprint(s.out, fableWalkPrompt)
		line, ok := readAnswerLine(s.in)
		if !ok {
			fmt.Fprintln(s.out)
			return "q"
		}
		switch answer := strings.ToLower(strings.TrimSpace(line)); answer {
		case "", "s", "q":
			return answer
		}
	}
}

// readAnswerLine reads one line a byte at a time: the sign-in's claude reads
// the same stdin next, so nothing past the line may be taken from it.
func readAnswerLine(in io.Reader) (string, bool) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := in.Read(b)
		if n == 1 && b[0] == '\n' {
			return string(line), true
		}
		if n == 1 {
			line = append(line, b[0])
		}
		if err != nil {
			return string(line), len(line) > 0
		}
	}
}

// signInErrors is a sign-in's stderr: it passes everything on and keeps the
// text, whose last Error line is the reason a failed sign-in names. claude's
// login is handed the stream underneath (loginStderr), so it keeps the
// terminal itself.
type signInErrors struct {
	io.Writer
	text strings.Builder
}

func (w *signInErrors) Write(p []byte) (int, error) {
	w.text.Write(p)
	return w.Writer.Write(p)
}

func (w *signInErrors) reason(code int) string {
	lines := strings.Split(w.text.String(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSuffix(strings.TrimPrefix(lines[i], ansiRed), ansiReset)
		if reason, ok := strings.CutPrefix(line, "Error: "); ok {
			return reason
		}
	}
	return fmt.Sprintf("exit %d", code)
}

// loginStderr is the stream claude's login writes its errors to: the
// terminal's own under a walk's signInErrors, never a pipe, which a browser
// it starts could hold open past the login's exit.
func loginStderr(w io.Writer) io.Writer {
	if e, ok := w.(*signInErrors); ok {
		return e.Writer
	}
	return w
}
