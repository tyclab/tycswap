// Package ccversion knows which Claude Code a machine has, how it got there,
// what the newest version its installer offers is, and what brings it up to
// date (DESIGN A27, the dashboard's Updates card).
//
// The install method is read from where the binary really lives: Anthropic's
// native installer (~/.local/bin/claude), npm (a node_modules path or the
// legacy ~/.claude/local), a Homebrew cask (a Caskroom path) or WinGet. The
// newest version is read from that method's own source — the native release
// channel, npm's dist-tags, the cask's formula — so an update is only
// announced where the same installer can deliver it, and the upgrade command
// is that installer's own: `claude update` for a native install, `npm
// install -g` for npm (`claude update` for the legacy ~/.claude/local, which
// keeps a node_modules of its own), `brew upgrade --cask` for Homebrew,
// `winget upgrade` for WinGet. A Claude Code installed some other way is
// still compared against the native channel, and `claude update`, Claude
// Code's own installer, is offered for it.
package ccversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/tyclab/tycswap/internal/session"
)

// Method is how a Claude Code install got onto the machine.
type Method string

// The install methods Classify tells apart.
const (
	Native   Method = "native"   // Anthropic's installer (~/.local/bin/claude)
	NPM      Method = "npm"      // npm global, or the legacy ~/.claude/local
	Homebrew Method = "homebrew" // a Homebrew cask
	WinGet   Method = "winget"
	Unknown  Method = "unknown"
)

// Label names the method for a person.
func (m Method) Label() string {
	switch m {
	case Native:
		return "the native installer"
	case NPM:
		return "npm"
	case Homebrew:
		return "Homebrew"
	case WinGet:
		return "WinGet"
	}
	return "an unknown installer"
}

// npmPackage is Claude Code's npm package, pinned to the latest tag.
const npmPackage = "@anthropic-ai/claude-code@latest"

// defaultCask is the Homebrew cask Claude Code's own instructions name.
const defaultCask = "claude-code"

// Command is one thing to run: an argv without a shell.
type Command struct {
	Argv []string
}

// String is the command as a person would type it (the program by name, not
// by its absolute path).
func (c Command) String() string {
	if len(c.Argv) == 0 {
		return ""
	}
	name := c.Argv[0]
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return strings.Join(append([]string{strings.TrimSuffix(name, ".exe")}, c.Argv[1:]...), " ")
}

// Installed is the Claude Code this machine runs.
type Installed struct {
	Path    string // where it was found
	Real    string // Path with symlinks resolved
	Version string // e.g. "2.1.280"; "" when --version said nothing usable
	Method  Method
	Cask    string // Homebrew: the cask token, e.g. "claude-code"
	Channel string // native/unknown: the user's stable/latest update channel
}

// Classify tells the method from where the binary really lives.
func Classify(real string) (Method, string) {
	p := strings.ReplaceAll(real, `\`, "/")
	lower := strings.ToLower(p)
	if i := strings.Index(p, "/Caskroom/"); i >= 0 {
		rest := p[i+len("/Caskroom/"):]
		if j := strings.IndexByte(rest, '/'); j > 0 {
			return Homebrew, rest[:j]
		}
		return Homebrew, defaultCask
	}
	switch {
	case strings.Contains(lower, "/node_modules/@anthropic-ai/claude-code/"),
		strings.Contains(lower, "/.claude/local/"),
		strings.Contains(lower, "/appdata/roaming/npm/"):
		return NPM, ""
	case strings.Contains(lower, "/.local/share/claude/"),
		strings.Contains(lower, "/.local/bin/claude"):
		return Native, ""
	case strings.Contains(lower, "/winget/"):
		return WinGet, ""
	}
	return Unknown, ""
}

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?`)

// ParseVersion finds the version in `claude --version` output
// ("2.1.280 (Claude Code)").
func ParseVersion(out string) string { return versionRe.FindString(out) }

// Newer reports whether version a is later than b.
func Newer(a, b string) bool {
	va, vb := "v"+strings.TrimPrefix(a, "v"), "v"+strings.TrimPrefix(b, "v")
	return semver.IsValid(va) && semver.IsValid(vb) && semver.Compare(va, vb) > 0
}

// Endpoints are where the newest versions are read.
type Endpoints struct {
	Downloads string // <base>/<latest|stable> is a plain-text version
	Homebrew  string // <base>/<cask>.json has "version"
	NPM       string // dist-tags JSON
}

// DefaultEndpoints are the public sources.
var DefaultEndpoints = Endpoints{
	Downloads: "https://downloads.claude.ai/claude-code-releases",
	Homebrew:  "https://formulae.brew.sh/api/cask",
	NPM:       "https://registry.npmjs.org/-/package/@anthropic-ai/claude-code/dist-tags",
}

// Env is the machine as this package sees it; tests replace every part.
type Env struct {
	GOOS      string
	Home      string
	Getenv    func(string) string
	LookPath  func(string) (string, error)
	Exists    func(string) bool
	Realpath  func(string) (string, error)
	ReadFile  func(string) ([]byte, error) // nil -> os.ReadFile
	Run       func(ctx context.Context, argv []string) (string, error)
	HTTP      *http.Client
	Endpoints Endpoints
}

// DefaultEnv is this process's machine.
func DefaultEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		GOOS:     runtime.GOOS,
		Home:     home,
		Getenv:   os.Getenv,
		LookPath: exec.LookPath,
		Exists: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && !fi.IsDir()
		},
		Realpath:  filepath.EvalSymlinks,
		ReadFile:  os.ReadFile,
		Run:       runOutput,
		HTTP:      http.DefaultClient,
		Endpoints: DefaultEndpoints,
	}
}

// candidates are the places Claude Code lives when PATH does not say: a
// server started from a launcher has the launcher's PATH, not the shell's.
func (e Env) candidates() []string {
	h := e.Home
	if e.GOOS == "windows" {
		return []string{
			filepath.Join(h, ".local", "bin", "claude.exe"),
			filepath.Join(e.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links", "claude.exe"),
			filepath.Join(e.Getenv("APPDATA"), "npm", "claude.cmd"),
		}
	}
	return []string{
		filepath.Join(h, ".local", "bin", "claude"),
		filepath.Join(h, ".claude", "local", "claude"),
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		"/home/linuxbrew/.linuxbrew/bin/claude",
		filepath.Join(h, ".npm-global", "bin", "claude"),
	}
}

// Locate is where Claude Code's binary is: PATH first, then the places the
// installers put it. "" means there is none.
func Locate(e Env) string {
	if p, err := e.LookPath("claude"); err == nil && p != "" {
		return p
	}
	for _, c := range e.candidates() {
		if e.Exists(c) {
			return c
		}
	}
	return ""
}

// Find locates Claude Code and asks it for its version. nil, nil means there
// is none.
func Find(ctx context.Context, e Env) (*Installed, error) {
	path := Locate(e)
	if path == "" {
		return nil, nil
	}
	real, err := e.Realpath(path)
	if err != nil {
		real = path
	}
	in := &Installed{Path: path, Real: real}
	in.Method, in.Cask = Classify(real)
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := e.Run(vctx, []string{path, "--version"})
	in.Version = ParseVersion(out)
	if in.Version == "" && err != nil {
		return in, fmt.Errorf("claude --version: %w", err)
	}
	if in.Method == Native || in.Method == Unknown {
		in.Channel, err = userChannel(e)
		if err != nil {
			return in, err
		}
	}
	return in, nil
}

// userChannel reads the channel /config and the native installer save in
// the user's settings.json. CLAUDE_CONFIG_DIR replaces ~/.claude. This is
// not a resolver for Claude Code's project or managed policy: the installer
// still applies those, and the host verifies the version after an update.
// A missing setting defaults to latest; unreadable or invalid settings are
// an error, rather than an invitation to offer the wrong channel.
func userChannel(e Env) (string, error) {
	dir := ""
	if e.Getenv != nil {
		dir = e.Getenv("CLAUDE_CONFIG_DIR")
	}
	if dir == "" {
		if e.Home == "" {
			return "latest", nil
		}
		dir = filepath.Join(e.Home, ".claude")
	}
	path := filepath.Join(dir, "settings.json")
	read := e.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	raw, err := read(path)
	if errors.Is(err, os.ErrNotExist) {
		return "latest", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Claude Code update channel from %s: %w", path, err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
		return "", fmt.Errorf("read Claude Code update channel from %s: settings must be a JSON object", path)
	}
	value, ok := settings["autoUpdatesChannel"]
	if !ok {
		return "latest", nil
	}
	var channel string
	if json.Unmarshal(value, &channel) != nil || channel != "latest" && channel != "stable" {
		return "", fmt.Errorf("read Claude Code update channel from %s: autoUpdatesChannel must be latest or stable", path)
	}
	return channel, nil
}

// Latest is the newest version the install's own method can deliver now: the
// cask's version for Homebrew, npm's latest tag, or the native release
// channel selected in user settings (also for an unknown install). WinGet
// follows the native latest feed.
func Latest(ctx context.Context, e Env, in *Installed) (string, error) {
	method, cask := Native, defaultCask
	if in != nil {
		method = in.Method
		if in.Cask != "" {
			cask = in.Cask
		}
	}
	switch method {
	case Homebrew:
		var c struct {
			Version string `json:"version"`
		}
		if err := getJSON(ctx, e.HTTP, e.Endpoints.Homebrew+"/"+cask+".json", &c); err != nil {
			return "", err
		}
		v, _, _ := strings.Cut(c.Version, ",") // casks may append a build id
		return checkVersion(v)
	case NPM:
		var tags map[string]string
		if err := getJSON(ctx, e.HTTP, e.Endpoints.NPM, &tags); err != nil {
			return "", err
		}
		return checkVersion(tags["latest"])
	default:
		channel := "latest"
		if method == Native || method == Unknown {
			if in != nil {
				channel = in.Channel
			} else {
				channel = ""
			}
			if channel == "" {
				var err error
				channel, err = userChannel(e)
				if err != nil {
					return "", err
				}
			}
		}
		body, err := get(ctx, e.HTTP, e.Endpoints.Downloads+"/"+channel)
		if err != nil {
			return "", err
		}
		return checkVersion(strings.TrimSpace(string(body)))
	}
}

func checkVersion(v string) (string, error) {
	if v == "" || ParseVersion(v) != v {
		return "", fmt.Errorf("unexpected version %q", v)
	}
	return v, nil
}

func get(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	if c == nil {
		c = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}

func getJSON(ctx context.Context, c *http.Client, url string, v any) error {
	body, err := get(ctx, c, url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return nil
}

// UpgradeCommand brings in up to date with its own installer. "claude" as
// the program means the installed binary (Run substitutes its path).
func UpgradeCommand(in *Installed) Command {
	if in == nil {
		return Command{}
	}
	switch in.Method {
	case NPM:
		// The legacy local install has a node_modules of its own, which
		// `npm install -g` does not touch; Claude Code's updater updates it.
		if legacyLocal(in.Real) {
			return Command{Argv: []string{"claude", "update"}}
		}
		return Command{Argv: []string{"npm", "install", "-g", npmPackage}}
	case Homebrew:
		cask := in.Cask
		if cask == "" {
			cask = defaultCask
		}
		return Command{Argv: []string{"brew", "upgrade", "--cask", cask}}
	case WinGet:
		return Command{Argv: []string{"winget", "upgrade", "--id", "Anthropic.ClaudeCode", "--exact"}}
	}
	return Command{Argv: []string{"claude", "update"}}
}

// legacyLocal reports whether real is in the legacy ~/.claude/local install.
func legacyLocal(real string) bool {
	return strings.Contains(strings.ToLower(strings.ReplaceAll(real, `\`, "/")), "/.claude/local/")
}

// InstallHint says, for a person, how Claude Code is installed on goos when
// there is none: Claude Code's own installer. It is a hint to type, never
// run by this package.
func InstallHint(goos string) string {
	if goos == "windows" {
		return "irm https://claude.ai/install.ps1 | iex"
	}
	return "curl -fsSL https://claude.ai/install.sh | bash"
}

// State is what the machine should do.
type State int

// The states a Status can be in.
const (
	// Checking: nothing known yet.
	Checking State = iota
	// Missing: no Claude Code on this machine.
	Missing
	// UpdateAvailable: the installer offers a newer version.
	UpdateAvailable
	// UpToDate: at the newest version the installer offers.
	UpToDate
	// CannotCheck: the newest version (or the installed one) could not be
	// read.
	CannotCheck
)

// Status is the outcome of a Check.
type Status struct {
	Installed *Installed
	Latest    string
	Err       error // why Latest (or the installed version) is not known
	// Checked: a Check ran (even one that found nothing).
	Checked bool
}

// State classifies the status.
func (s Status) State() State {
	switch {
	case !s.Checked:
		return Checking
	case s.Installed == nil:
		return Missing
	case s.Latest == "" || s.Installed.Version == "":
		return CannotCheck
	case Newer(s.Latest, s.Installed.Version):
		return UpdateAvailable
	}
	return UpToDate
}

// Check finds the installed Claude Code and the newest version its installer
// offers. A missing Claude Code is not an error: Latest is not asked for.
func Check(ctx context.Context, e Env) Status {
	st := Status{Checked: true}
	in, err := Find(ctx, e)
	st.Installed = in
	if err != nil {
		st.Err = err
		return st
	}
	if in == nil {
		return st
	}
	latest, lerr := Latest(ctx, e, in)
	st.Latest = latest
	if lerr != nil && st.Err == nil {
		st.Err = lerr
	}
	return st
}

// Run executes c. "claude" as the program means in.Path.
func Run(ctx context.Context, e Env, c Command, in *Installed) (string, error) {
	argv := append([]string(nil), c.Argv...)
	if len(argv) == 0 {
		return "", errors.New("nothing to run")
	}
	if argv[0] == "claude" && in != nil && in.Path != "" {
		argv[0] = in.Path
	}
	return e.Run(ctx, argv)
}

// runOutput runs argv and returns its combined output.
func runOutput(ctx context.Context, argv []string) (string, error) {
	cmd := session.CLICommand(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
