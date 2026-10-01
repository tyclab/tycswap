// webcmd.go — `tycswap web`: wire the dashboard's façades, bind loopback,
// print the one-time URL, open the browser, serve until SIGINT/SIGTERM
// (DESIGN A25).
package cli

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	neturl "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/browser"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/web"
)

// Seams for the opener's tests: which platform's rules apply, and what
// finally hands a URL to the browser.
var (
	browserGOOS   = runtime.GOOS
	launchBrowser = browser.Open
)

// redirectFileTTL is how long the redirect page stays on disk: the browser
// must still find it when it gets round to opening it (a cold start, or on
// WSL a hop to the Windows side), and it carries a token, so not longer.
const redirectFileTTL = 30 * time.Second

// openBrowser opens the dashboard URL, which carries the one-time launch
// token.
//
// On Unix — WSL included — a browser launcher's argv is readable by every
// local user (/proc, ps), so the token never goes on a command line there: it
// is written into a 0600 HTML page under the temp directory that redirects to
// the URL, and the launcher gets that page's file:// URL. On WSL the launcher
// chain reaches the Windows browser through wslview, or an xdg-open, that
// translates the Linux path. The token is single-use on the server side as
// well, so even a captured URL is dead after the first open. If the page
// cannot be written, the plain URL is the last resort.
//
// On Windows the URL goes to the browser directly: a file: page there depends
// on .html being associated with a browser and on file URLs not being blocked
// by policy, while an http link always reaches the default browser; and
// another non-administrator user cannot read a process's command line.
var openBrowser = func(url string) error {
	if browserGOOS == "windows" {
		return launchBrowser(url)
	}
	b := brand.Sanitized()
	f, err := os.CreateTemp("", b.RedirectFilePrefix+"*.html")
	if err != nil {
		return launchBrowser(url) // last resort: the plain URL
	}
	name := f.Name()
	_ = f.Chmod(0o600)
	page := "<!doctype html><meta charset=\"utf-8\"><meta http-equiv=\"refresh\" content=\"0;url=" + html.EscapeString(url) + "\"><title>" + b.DisplayName + "</title><p>Opening the " + b.DisplayName + " dashboard…</p>"
	if _, err := f.WriteString(page); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return launchBrowser(url)
	}
	_ = f.Close()
	time.AfterFunc(redirectFileTTL, func() { _ = os.Remove(name) })
	return launchBrowser(fileURL(name))
}

// fileURL is the file: URL of an absolute path, escaped: "file:///tmp/a%20b.html"
// (and "file:///C:/…" for a drive path), never the raw path glued to "file://".
func fileURL(path string) string {
	p := strings.ReplaceAll(filepath.ToSlash(path), `\`, "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&neturl.URL{Scheme: "file", Path: p}).String()
}

// notifyContext is signal.NotifyContext, a seam for tests.
var notifyContext = signal.NotifyContext

// webCommand handles `tycswap web [--port N] [--no-open] [--interval SECONDS] [--debug]`.
func webCommand(prog string, argv []string, s ioStreams) int {
	wprog := prog + " web"
	port := 0
	interval := 5.0
	var noOpen, debug bool
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		val := func(name string) (string, bool) {
			if strings.HasPrefix(tok, name+"=") {
				return tok[len(name)+1:], true
			}
			if i+1 >= len(argv) {
				return "", false
			}
			i++
			return argv[i], true
		}
		switch {
		case tok == "--no-open":
			noOpen = true
		case tok == "--debug":
			debug = true
		case tok == "--port" || strings.HasPrefix(tok, "--port="):
			v, ok := val("--port")
			if !ok {
				return subError(wprog, s.err, "argument --port: expected one argument")
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 65535 {
				return subError(wprog, s.err, "argument --port: invalid int value: '"+v+"' (0-65535)")
			}
			port = n
		case tok == "--interval" || strings.HasPrefix(tok, "--interval="):
			v, ok := val("--interval")
			if !ok {
				return subError(wprog, s.err, "argument --interval: expected one argument")
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f <= 0 || f > 3600 {
				return subError(wprog, s.err, "argument --interval: invalid number of seconds: '"+v+"' (0-3600, exclusive of 0)")
			}
			interval = f
		case tok == "-h" || tok == "--help":
			printer.ForceUTF8Output()
			return renderWebHelp(prog, s.out)
		default:
			return subError(wprog, s.err, "unrecognized arguments: "+tok)
		}
	}
	srv, auto, code := newDashboard(interval, debug, s)
	if code != 0 {
		return code
	}
	url, err := srv.Start(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		errorTo(s.err, "Error: "+err.Error())
		return 1
	}
	fmt.Fprintf(s.err, "Dashboard: %s\n", url)
	if !noOpen {
		if err := openBrowser(url); err != nil {
			fmt.Fprintln(s.err, "Could not open a browser; visit the URL above.")
		}
	}
	fmt.Fprintln(s.err, "Press Ctrl-C to stop.")
	ctx, stop := notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := srv.Serve(ctx)
	if auto.View().Running {
		_ = auto.Stop()
	}
	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		errorTo(s.err, "Error: "+serveErr.Error())
		return 1
	}
	if ctx.Err() != nil {
		return 130
	}
	return 0
}

// newDashboard constructs the switcher and every façade the dashboard needs
// and returns the unstarted server. A non-zero code means the error was
// already reported on s.err.
func newDashboard(interval float64, debug bool, s ioStreams) (*web.Server, *autoFacade, int) {
	sw, err := constructSwitcher(debug, s.err)
	if err != nil {
		errorTo(s.err, "Error: "+err.Error())
		return nil, nil, 1
	}
	if code, blocked := guardRoot(s.err); blocked {
		return nil, nil, code
	}
	auto := newAutoFacade(sw)
	srv, err := web.New(web.Deps{
		Facade:     sw,
		Accounts:   webAccounts{sw},
		Settings:   settingsFacade{root: sw.BackupDir()},
		Auto:       auto,
		AutoEvents: auto.Events(),
		Transfer:   transferFacade{sw: sw},
		Sessions:   web.SessionsIn(sw.BackupDir()),
		Kill:       web.DefaultKill,
		Interval:   time.Duration(interval * float64(time.Second)),
		Logger: func(m string) {
			if debug {
				fmt.Fprintln(s.err, m)
			}
		},
	})
	if err != nil {
		errorTo(s.err, "Error: "+err.Error())
		return nil, nil, 1
	}
	return srv, auto, 0
}

func renderWebHelp(prog string, out io.Writer) int {
	fmt.Fprintf(out, `usage: %[1]s web [-h] [--port N] [--no-open] [--interval SECONDS] [--debug]

Serve the local dashboard on 127.0.0.1 and open it in the browser. The URL
carries a one-time token; only the browser that opens it can drive the page.

options:
  --port N          fixed port (default 0 = any free port)
  --no-open         print the URL only
  --interval S      live-state poll interval in seconds (default 5)
  --debug           log errors to stderr
`, prog)
	return 0
}
