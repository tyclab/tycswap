// Package keychain wraps the macOS /usr/bin/security CLI for generic passwords.
//
// Implements spec 03§4 (macos_keychain.py). The exact argv, absolute binary
// path, hex -X encoding, single trailing-\n strip, rc 44 handling and 5s timeout
// are byte-compatibility contracts with Claude-Code-seeded items. A real Security
// client (with an exec seam for tests) and an in-memory Fake are provided.
package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// SecurityStdinLineLimit is security -i's fgets buffer minus 64 bytes of
	// headroom (4096-64). Set refuses a longer command (TooLarge).
	SecurityStdinLineLimit = 4096 - 64
	notFoundRC             = 44
	// timeout bounds every security spawn.
	timeout = 5 * time.Second
	// securityBin is the pinned absolute path to Apple's system binary.
	securityBin = "/usr/bin/security"
)

const (
	BackupService = "tycswap"
	CodexService  = "tycswap-codex"
)

const (
	OldBackupService = "claude-swap"
	OldCodexService  = "claude-swap-codex"
)

// ErrInvalidName is returned (wrapped) when a service or account name holds a
// control character. Set feeds `security -i` a command line on stdin, so a
// CR or LF in a name would end that command and start another; every Security
// call refuses such a name before spawning anything.
var ErrInvalidName = errors.New("keychain item name contains a control character")

func ValidateName(service, account string) error {
	for _, f := range [...]struct{ what, v string }{{"service", service}, {"account", account}} {
		if !utf8.ValidString(f.v) {
			return fmt.Errorf("%w: %s %q is not valid UTF-8", ErrInvalidName, f.what, f.v)
		}
		for _, r := range f.v {
			if unicode.IsControl(r) {
				return fmt.Errorf("%w: %s %q", ErrInvalidName, f.what, f.v)
			}
		}
	}
	return nil
}

// KeychainClient is the seam every credential store uses.
type KeychainClient interface {
	Get(service, account string) (string, bool, error)
	Set(service, account, password string) error
	Delete(service, account string) error
	// Exists reports whether the item exists without decrypting it; never errors.
	Exists(service, account string) bool
}

// KeychainError is a security invocation failure other than "not found". It is
// classified as "Keychain unusable" by IsUnusable.
//
// TooLarge marks the one failure that says nothing about the Keychain itself:
// a Set payload too large for `security -i`'s stdin line. Callers store such a
// secret elsewhere, but must not conclude the Keychain is down (IsTooLarge).
type KeychainError struct {
	Msg      string
	TooLarge bool
}

func (e *KeychainError) Error() string { return e.Msg }

func keychainErrorf(format string, a ...any) *KeychainError {
	return &KeychainError{Msg: fmt.Sprintf(format, a...)}
}

// IsUnusable reports whether err should route the caller to file storage rather
// than being treated as a programming bug (KEYCHAIN_ERRORS parity): a
// KeychainError (including a converted timeout), a context deadline, or a
// missing security binary.
func IsUnusable(err error) bool {
	if err == nil {
		return false
	}
	var ke *KeychainError
	if errors.As(err, &ke) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ee *exec.Error
	if errors.As(err, &ee) {
		return true
	}
	return false
}

// IsTooLarge reports whether err is Set's refusal of a payload that does not fit
// `security -i`'s stdin line.
func IsTooLarge(err error) bool {
	var ke *KeychainError
	return errors.As(err, &ke) && ke.TooLarge
}

func AccountName() string {
	if u := getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "claude-code-user"
}

// quote wraps a value in double quotes for a security -i stdin command line,
// backslash-escaping embedded backslashes and double quotes.
func quote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// execResult is one security invocation's captured output.
type execResult struct {
	stdout string
	stderr string
	rc     int
}

type execFunc func(ctx context.Context, argv []string, stdin string) (execResult, error)

// Security is the real KeychainClient. Zero value is usable; Exec is a test seam.
type Security struct {
	// Path overrides the security binary path (default /usr/bin/security).
	Path    string
	Timeout time.Duration
	Exec    execFunc
}

func (s Security) bin() string {
	if s.Path != "" {
		return s.Path
	}
	return securityBin
}

func (s Security) budget() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return timeout
}

// errInTests is the real security binary's answer inside a test binary,
// before it starts: a test never reads or changes the runner's login Keychain.
var errInTests = errors.New("keychain: the real login Keychain is not reachable from tests; give the test a fake (keychain.NewFake)")

// call runs a security invocation, converting a start failure or timeout into a
// KeychainError.
func (s Security) call(argv []string, stdin string) (execResult, error) {
	runner := s.Exec
	if runner == nil {
		if testing.Testing() && s.bin() == securityBin {
			return execResult{}, errInTests
		}
		runner = realExec
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.budget())
	defer cancel()
	res, err := runner(ctx, argv, stdin)
	// Classify by the command's actual outcome, mirroring Python's
	// subprocess.run: TimeoutExpired fires only when the process is killed by
	// the deadline, never when the process completed and produced a result. A
	// non-nil error means the process could not be run or was killed; only then
	// can the deadline have discarded output. A result completing at ~the
	// deadline (nil err) is honored even if ctx has since expired.
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return execResult{}, keychainErrorf("security %s timed out after %s", commandName(argv), s.budget())
		}
		return execResult{}, err
	}
	return res, nil
}

func commandName(argv []string) string {
	if len(argv) >= 2 {
		return argv[1]
	}
	return "security"
}

func (s Security) Get(service, account string) (string, bool, error) {
	if err := ValidateName(service, account); err != nil {
		return "", false, err
	}
	res, err := s.call([]string{s.bin(), "find-generic-password", "-a", account, "-w", "-s", service}, "")
	if err != nil {
		return "", false, err
	}
	switch res.rc {
	case 0:
		return strings.TrimSuffix(res.stdout, "\n"), true, nil
	case notFoundRC:
		return "", false, nil
	default:
		return "", false, keychainErrorf(
			"security find-generic-password failed (rc=%d): %s", res.rc, strings.TrimSpace(res.stderr))
	}
}

// Exists reports whether an item exists via an attribute-only lookup (no -w, so
// nothing is decrypted). Never raises: rc 44, error exits, timeouts and a
// missing binary all return false.
func (s Security) Exists(service, account string) bool {
	if ValidateName(service, account) != nil {
		return false
	}
	res, err := s.call([]string{s.bin(), "find-generic-password", "-a", account, "-s", service}, "")
	if err != nil {
		return false
	}
	return res.rc == 0
}

// FitsStdin reports whether Set can store password: the secret only ever rides
// on `security -i`'s stdin, and a command line over SecurityStdinLineLimit
// makes Set refuse with a TooLarge KeychainError. It measures the exact
// command line Set builds.
func FitsStdin(service, account, password string) bool {
	return len(setCommand(service, account, password)) <= SecurityStdinLineLimit
}

func setCommand(service, account, password string) string {
	return fmt.Sprintf("add-generic-password -U -a %s -s %s -X %s\n",
		quote(account), quote(service), toHex(password))
}

// Set creates or updates an item (-U). The secret is hex-encoded (-X) and rides
// on stdin, never in argv, where every local user could read it through ps.
// A payload whose command line exceeds the line-buffer limit is refused with
// a TooLarge KeychainError and nothing is run. IsUnusable is true for that
// error, so callers take their file fallback.
func (s Security) Set(service, account, password string) error {
	if err := ValidateName(service, account); err != nil {
		return err
	}
	command := setCommand(service, account, password)
	if len(command) > SecurityStdinLineLimit {
		return &KeychainError{
			Msg: fmt.Sprintf("secret for %s/%s is too large for the Keychain's stdin interface (%d > %d bytes); "+
				"refusing to pass it on the command line", service, account, len(command), SecurityStdinLineLimit),
			TooLarge: true,
		}
	}
	res, err := s.call([]string{s.bin(), "-i"}, command)
	if err != nil {
		return err
	}
	if res.rc != 0 {
		return keychainErrorf(
			"security add-generic-password failed (rc=%d): %s", res.rc, strings.TrimSpace(res.stderr))
	}
	return nil
}

// Delete removes an item; rc 0 and rc 44 (already absent) both succeed.
func (s Security) Delete(service, account string) error {
	if err := ValidateName(service, account); err != nil {
		return err
	}
	res, err := s.call([]string{s.bin(), "delete-generic-password", "-a", account, "-s", service}, "")
	if err != nil {
		return err
	}
	if res.rc == 0 || res.rc == notFoundRC {
		return nil
	}
	return keychainErrorf(
		"security delete-generic-password failed (rc=%d): %s", res.rc, strings.TrimSpace(res.stderr))
}

// realExec runs the security binary for real.
func realExec(ctx context.Context, argv []string, stdin string) (execResult, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ctx.Err() == context.DeadlineExceeded && !ee.Exited() {
				return execResult{}, ctx.Err()
			}
			return execResult{stdout: out.String(), stderr: errb.String(), rc: ee.ExitCode()}, nil
		}
		// Could not start (missing binary, etc.) or context cancelled before start.
		if ctx.Err() == context.DeadlineExceeded {
			return execResult{}, ctx.Err()
		}
		return execResult{}, err
	}
	return execResult{stdout: out.String(), stderr: errb.String(), rc: 0}, nil
}

var _ KeychainClient = Security{}
