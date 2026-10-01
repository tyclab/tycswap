// addlogin.go — `cswap add --login [--switch] [-- <claude auth login args>]`.
//
// Adding a second Claude account used to mean logging the live one out, which
// discards its refresh token. --login runs Claude Code's own `claude auth
// login` in a scratch CLAUDE_CONFIG_DIR instead and stores what it leaves
// there, the same way `cswap codex login` stores a codex login: the live login
// is never touched, and the account recorded as active stays the one that is
// live. --switch then makes the new account live through the regular switch,
// so the outgoing credential is written back as on any `cswap switch <n>`.
//
// The login waits on a browser, so it runs before any store or roster lock is
// taken: a lock held across it would fail every other cswap invocation and the
// auto engine for as long as the browser tab stays open.
package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"git.dpemmons.com/dpemmons/cswap/internal/keychain"
	"git.dpemmons.com/dpemmons/cswap/internal/lifecycle"
	"git.dpemmons.com/dpemmons/cswap/internal/paths"
	"git.dpemmons.com/dpemmons/cswap/internal/platform"
)

// claudeLookPath resolves the claude binary the way `cswap run` does
// (shutil.which, so a Windows .cmd shim resolves). A seam for the tests.
var claudeLookPath = exec.LookPath

// runClaudeLogin runs the resolved claude binary directly — never through a
// shell, so a shell function or alias named `claude` cannot add flags cswap
// never chose — with env as its whole environment and the terminal inherited,
// so the user can finish the browser flow. It returns the exit status.
var runClaudeLogin = func(binary string, args, env []string, s ioStreams) (int, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, s.out, s.err
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code > 0 {
			return code, nil
		}
		return 1, nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

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
	errSwitchWithoutLogin = "--switch goes with --login: the live login is already the one add stores"
	errTailWithoutLogin   = "arguments after -- go with --login: they are claude's login arguments"
)

// addArgs is `cswap add`'s argv split into the parts --login owns and the rest,
// which the main parser still reads (--slot, --alias, --debug, --help, and the
// usual refusals for flags add does not take).
type addArgs struct {
	login, switchAfter bool
	rest               []string // add's own flags, for the main parser
	tail               []string // everything after the first "--"
}

func splitAddArgs(argv []string) addArgs {
	var a addArgs
	for i, tok := range argv {
		if tok == "--" {
			a.tail = argv[i+1:]
			break
		}
		switch tok {
		case "--login":
			a.login = true
		case "--switch":
			a.switchAfter = true
		default:
			a.rest = append(a.rest, tok)
		}
	}
	return a
}

// addCommand intercepts `cswap add` (and its legacy spelling --add-account).
// handled is false for a plain add, which the main parser dispatches as before.
func addCommand(prog string, argv []string, s ioStreams) (code int, handled bool) {
	a := splitAddArgs(argv)
	if !a.login {
		switch {
		case a.switchAfter:
			errorTo(s.err, "Error: "+errSwitchWithoutLogin)
			return 1, true
		case len(a.tail) > 0:
			errorTo(s.err, "Error: "+errTailWithoutLogin)
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
	sw, err := constructSwitcher(p.debug, s.err)
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	if code, blocked := guardRoot(s.err); blocked {
		return code
	}
	setSigintJSON(false)

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
	scratch, removeScratch, err := makeLoginScratch(paths.GetBackupRoot(), kc)
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	defer removeScratch()

	args := append([]string{"auth", "login"}, a.tail...)
	code, err := runClaudeLogin(binary, args, loginEnv(os.Environ(), scratch), s)
	if err != nil || code != 0 {
		return renderDomainError(lifecycle.ErrLoginIncomplete(), false, s.out, s.err)
	}
	src := lifecycle.LoginDir(scratch, kc)
	if err := lifecycle.CheckLogin(src); err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}

	num, err := sw.AddAccountFromLogin(src, p.slot, false, p.alias)
	if err != nil {
		return renderDomainError(err, false, s.out, s.err)
	}
	// The stored copy is all that is needed from here on; drop the scratch
	// before the switch so a switch failure cannot leave a login behind.
	removeScratch()

	if a.switchAfter {
		if _, err := sw.SwitchToForce(num, false, false); err != nil {
			return renderDomainError(err, false, s.out, s.err)
		}
	}
	maybeUpdateNotice(p, sw.BackupDir(), sw.Platform(), s.err)
	return 0
}

// makeLoginScratch creates the private scratch profile the login runs in,
// under the backup root so it shares the store's filesystem and permissions.
// The returned remove is idempotent and is also registered with the SIGINT
// cleanups, so a Ctrl-C during the browser wait leaves no login behind. With
// a Keychain (macOS) it first deletes the item Claude Code keyed by the
// scratch dir: once the dir is gone that item's name can no longer be derived
// from anything cswap keeps, so it would outlive the login. A "not found"
// delete is success, and any other failure is ignored.
func makeLoginScratch(root string, kc keychain.KeychainClient) (dir string, remove func(), err error) {
	dir, err = os.MkdirTemp(root, "login.")
	if err != nil {
		return "", nil, err
	}
	if !platform.IsWindows() {
		if err := os.Chmod(dir, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", nil, err
		}
	}
	var once sync.Once
	var id uint64
	remove = func() {
		once.Do(func() {
			lifecycle.Unregister(id)
			if kc != nil {
				_ = kc.Delete(lifecycle.LoginKeychainService(dir), keychain.AccountName())
			}
			_ = os.RemoveAll(dir)
		})
	}
	id = lifecycle.RegisterCleanup(remove)
	return dir, remove, nil
}

// loginEnv is the parent environment with CLAUDE_CONFIG_DIR pointing at the
// scratch profile. Any CLAUDE_CONFIG_DIR already set — a custom one, or the
// session profile of a `cswap env`-pinned shell — is replaced, so the login
// can never land in a live profile.
func loginEnv(parent []string, scratch string) []string {
	const key = "CLAUDE_CONFIG_DIR"
	env := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if name == key || (runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, key+"="+scratch)
}
