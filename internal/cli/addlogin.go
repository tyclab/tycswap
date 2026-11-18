// addlogin.go — `tycswap add --login [--switch] [-- <claude auth login args>]`.
//
// Adding a second Claude account used to mean logging the live one out, which
// discards its refresh token. --login runs Claude Code's own `claude auth
// login` in a scratch CLAUDE_CONFIG_DIR instead and stores what it leaves
// there, the same way `tycswap codex login` stores a codex login: the live login
// is never touched, and the account recorded as active stays the one that is
// live. --switch then makes the new account live through the regular switch,
// so the outgoing credential is written back as on any `tycswap switch <n>`.
//
// The login waits on a browser, so it runs before any store or roster lock is
// taken: a lock held across it would fail every other tycswap invocation and the
// auto engine for as long as the browser tab stays open.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/core"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/launch"
	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
)

// claudeLookPath resolves the claude binary on PATH (so a Windows .cmd shim
// resolves). A seam for the tests.
var claudeLookPath = exec.LookPath

// loginKeychain is the Keychain a scratch login may write its credential to
// instead of <scratch>/.credentials.json: Claude Code does on macOS, under an
// item keyed by the config dir. nil elsewhere. A seam so the tests drive the
// macOS fallback with a fake on any platform.
var loginKeychain = func() keychain.KeychainClient {
	if platform.Detect() == platform.MacOS {
		return keychain.Security{}
	}
	return nil
}

const (
	errSwitchWithoutLogin  = "--switch goes with --login: the live login is already the one add stores"
	errTailWithoutLogin    = "arguments after -- go with --login: they are claude's login arguments"
	errLineageWithoutLogin = "--lineage goes with --login"
	errAliasFable          = "--alias names a new account; a Fable login is stored for an account already added"
)

type addArgs struct {
	login, switchAfter bool
	// lineage is --lineage's value (DESIGN A101); lineageErr a usage error for it.
	lineage, lineageErr string
	rest                []string // add's own flags, for the main parser
	tail                []string // everything after the first "--"
}

func splitAddArgs(argv []string) addArgs {
	var a addArgs
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if tok == "--" {
			a.tail = argv[i+1:]
			break
		}
		switch {
		case tok == "--login":
			a.login = true
		case tok == "--switch":
			a.switchAfter = true
		case strings.HasPrefix(tok, "--lineage="):
			a.setLineage(strings.TrimPrefix(tok, "--lineage="))
		case tok == "--lineage" && i+1 < len(argv) && !isOptionLike(argv[i+1]):
			i++
			a.setLineage(argv[i])
		case tok == "--lineage":
			a.lineageErr = "argument --lineage: expected one argument"
		default:
			a.rest = append(a.rest, tok)
		}
	}
	return a
}

func (a *addArgs) setLineage(v string) {
	a.lineage = v
	if v != "default" && v != "fable" && a.lineageErr == "" {
		a.lineageErr = "unknown lineage: " + v + " (default or fable)"
	}
}

// addCommand intercepts `tycswap add` (and its legacy spelling --add-account).
// handled is false for a plain add, which the main parser dispatches as before.
func addCommand(prog string, argv []string, s ioStreams) (code int, handled bool) {
	a := splitAddArgs(argv)
	if a.lineageErr != "" {
		return argError(prog, s.err, a.lineageErr).code, true
	}
	if !a.login {
		switch {
		case a.switchAfter:
			errorTo(s.err, "Error: "+errSwitchWithoutLogin)
			return 1, true
		case len(a.tail) > 0:
			errorTo(s.err, "Error: "+errTailWithoutLogin)
			return 1, true
		case a.lineage != "":
			errorTo(s.err, "Error: "+errLineageWithoutLogin)
			return 1, true
		}
		return 0, false
	}

	pr := parseArgs(prog, append([]string{"--add-account"}, a.rest...), s.out, s.err)
	if pr.done {
		return pr.code, true
	}
	if vr := crossFlagValidate(prog, pr.p, s.err); vr.done {
		return vr.code, true
	}
	return addLogin(pr.p, a, s), true
}

// addLogin runs the login in a scratch profile and stores the result.
func addLogin(p *parsed, a addArgs, s ioStreams) int {
	// Refuse before constructing a store or starting Claude: validating the
	// result cannot undo a Console key that the subprocess already created.
	for _, arg := range a.tail {
		if name, _, _ := strings.Cut(arg, "="); name == "--console" {
			errorTo(s.err, "Error: --console is not supported by add --login; use tycswap add-token for API keys")
			return 1
		}
	}
	sw, err := constructSwitcher(p.debug, s.err)
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	if code, blocked := guardRoot(s.err); blocked {
		return code
	}
	setSigintJSON(false)
	fable := a.lineage == "fable"
	if fable {
		if code, refused := fableLoginPreflight(sw, p, s); refused {
			return code
		}
	}

	binary, err := claudeLookPath("claude")
	if err != nil || binary == "" {
		errorTo(s.err, "Error: 'claude' was not found on PATH. Install Claude Code first.")
		return 1
	}
	if abs, aerr := filepath.Abs(binary); aerr == nil {
		binary = abs
	}

	if err := sw.Store.SetupDirectories(); err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	kc := loginKeychain()
	if err := retryLoginCleanups(paths.GetBackupRoot(), kc); err != nil {
		errorTo(s.err, "Warning: "+err.Error())
	}
	scratch, removeScratch, err := makeLoginScratch(paths.GetBackupRoot(), kc)
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	defer func() {
		if err := removeScratch(); err != nil {
			errorTo(s.err, "Warning: "+err.Error())
		}
	}()

	args := append([]string{"auth", "login", "--claudeai"}, a.tail...)
	if err := launch.CheckCmdShimArgs(binary, args); err != nil {
		return renderDomainError(cerr.Validation("%s", err.Error()), false, s.out, s.err)
	}
	// Auth override variables (ANTHROPIC_API_KEY, …) make claude skip the account login.
	login, err := startClaudeLogin(binary, args, loginEnv(launch.ScrubAuthOverrides(os.Environ()), scratch), s)
	if err != nil {
		return renderDomainError(lifecycle.ErrLoginIncomplete(), false, s.out, s.err)
	}
	src := lifecycle.LoginDir(scratch, kc)
	store := func() (string, error) { return sw.AddAccountFromLogin(src, p.slot, false, p.alias) }
	if fable {
		store = func() (string, error) { return sw.AddFableLogin(src, p.slot) }
	}
	num, err := storeLoginWhenComplete(src, login, store, s)
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	// The stored copy is all that is needed from here on; drop the scratch
	// before the switch so a switch failure cannot leave a login behind.
	_ = removeScratch()
	if fable {
		if err := activateFableLogin(sw, num, a.switchAfter, s); err != nil {
			return renderDomainError(err, false, s.out, s.err)
		}
		maybeUpdateNotice(p, sw.BackupDir(), sw.Platform(), s.err)
		return 0
	}

	data, err := sw.ReadSequence()
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	// A login as the live account renews it: without the activation the live
	// store keeps the old credential and `list` reads that one (A65). The live
	// account is the credential's, not the one ~/.claude.json names: a Fable
	// session writes its own account there (A101).
	renewsLive := sw.ResolveLiveSlot(data).Slot == num
	if a.switchAfter || renewsLive {
		if _, err := sw.SwitchToForce(num, false, renewsLive); err != nil {
			return renderDomainError(err, false, s.out, s.err)
		}
	}
	maybeUpdateNotice(p, sw.BackupDir(), sw.Platform(), s.err)
	return 0
}

// fableLoginPreflight refuses a Fable sign-in before Claude starts, and with
// --slot names the account to sign in as (DESIGN A101).
func fableLoginPreflight(sw *core.Switcher, p *parsed, s ioStreams) (int, bool) {
	if p.alias != nil {
		errorTo(s.err, "Error: "+errAliasFable)
		return 1, true
	}
	if _, err := sw.Store.ForFable(); err != nil {
		return renderDomainError(err, false, s.out, s.err), true
	}
	if p.slot == nil {
		return 0, false
	}
	data, err := sw.ReadSequence()
	if err != nil {
		return renderDomainError(err, false, s.out, s.err), true
	}
	_, email, _, err := sw.ResolveAccountFrom(data, strconv.Itoa(*p.slot))
	if err != nil {
		return renderDomainError(err, false, s.out, s.err), true
	}
	fmt.Fprintln(s.out, "Sign in as "+email+" in the browser.")
	return 0, false
}

// activateFableLogin installs a stored Fable login by a switch by hand when
// the Fable store holds none, or with --switch.
func activateFableLogin(sw *core.Switcher, num string, switchAfter bool, s ioStreams) error {
	f, err := sw.Store.ForFable()
	if err != nil {
		return err
	}
	if !switchAfter && f.HasLiveLogin() {
		return nil
	}
	res, err := sw.SwitchFableBy(num, false, cliActor())
	if err != nil {
		return err
	}
	if res["switched"] == true {
		fmt.Fprintln(s.out, lifecycle.FableStoreUses(num))
	}
	return nil
}

// loginEnv is the parent environment with CLAUDE_CONFIG_DIR pointing at the
// scratch profile. Any CLAUDE_CONFIG_DIR already set — a custom one, or the
// former session profile a shell still names — is replaced, so the login
// can never land in a live profile. CLAUDE_SECURESTORAGE_CONFIG_DIR is dropped
// too: Claude Code names its credential storage after it in place of the
// config dir, and after the default `~/.claude` storage (the unhashed items)
// when it is set but empty (DESIGN A56).
func loginEnv(parent []string, scratch string) []string {
	const key = "CLAUDE_CONFIG_DIR"
	env := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if loginDirVar(name, key) || loginDirVar(name, "CLAUDE_SECURESTORAGE_CONFIG_DIR") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, key+"="+scratch)
}

// loginDirVar reports whether the variable name is want, case-insensitively on
// Windows.
func loginDirVar(name, want string) bool {
	return name == want || (runtime.GOOS == "windows" && strings.EqualFold(name, want))
}
