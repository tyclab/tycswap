// Package store is the account-store substrate: sequence.json, backup paths, credential/config proxies, identity resolution, locks.
package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/migrations"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/usage"
	"github.com/tyclab/tycswap/internal/wincred"
)

type Store struct {
	Home           string
	SequenceFile   string
	ConfigsDir     string
	CredentialsDir string
	LockFile       string
	Platform       platform.Platform

	Creds credstore.Store
	Usage *usage.Store
	Lock  *filelock.FileLock
	OAuth oauth.Client
	Log   *logging.Logger
	Clk   clock.Clock

	backupDir              string
	kc                     keychain.KeychainClient
	wc                     wincred.Client
	group                  groups.ID
	groupIntent            groups.Intent
	groupHandoff           bool
	sharedCreds            credstore.Store
	defaultProfileDir      string
	defaultUnpinned        bool
	defaultKeychainService string
}

// Options carries the injectable seams for New. All are optional: a zero
// Options builds a production Store (real clock, real Keychain via
// /usr/bin/security, real Windows Credential Manager stub, notices to
// os.Stderr, no OAuth network client). Tests inject fakes.
type Options struct {
	Debug bool
	// Clock is the wall clock for timestamps and cooldowns; default clock.System.
	Clock clock.Clock
	// Keychain is the macOS Keychain client; default keychain.Security{}.
	Keychain keychain.KeychainClient
	OAuth    oauth.Client
	// WinCred is the legacy Windows Credential Manager reader; default
	// wincred.New() (the always-not-found stub off Windows).
	WinCred wincred.Client
	// Migration notices fire before the CLI knows --json, so they go straight to stderr.
	Stderr            io.Writer
	DefaultProfileDir string
}

// New reproduces ClaudeAccountSwitcher.__init__ exactly (spec 07§5.6, DESIGN
// Appendix). Only the legacy-directory migration (step 3) can return an error
// that aborts construction; the registry migrations (step 7) never do.
func New(opts Options) (*Store, error) {
	clk := opts.Clock
	if clk == nil {
		clk = clock.System{}
	}
	kc := opts.Keychain
	if kc == nil {
		kc = keychain.Security{}
	}
	wc := opts.WinCred
	if wc == nil {
		wc = wincred.New()
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	plat := platform.Detect()

	backupDir := paths.GetBackupRoot()
	defaultProfileDir := opts.DefaultProfileDir
	defaultUnpinned := opts.DefaultProfileDir == "" && os.Getenv("CLAUDE_CONFIG_DIR") == ""
	if defaultProfileDir == "" {
		defaultProfileDir = paths.GetClaudeConfigHome()
		if sessprofile.IsSessionProfileDir(backupDir, defaultProfileDir) {
			defaultProfileDir = os.Getenv("TYCSWAP_DEFAULT_PROFILE_DIR")
			defaultUnpinned = os.Getenv("TYCSWAP_DEFAULT_PROFILE_UNPINNED") == "true" || defaultProfileDir == ""
			if defaultProfileDir == "" || sessprofile.IsSessionProfileDir(backupDir, defaultProfileDir) {
				defaultProfileDir = filepath.Join(home, ".claude")
			}
		}
	}
	defaultKeychainService := "Claude Code-credentials"
	if !defaultUnpinned {
		defaultKeychainService = sessprofile.KeychainServiceName(defaultProfileDir)
	}
	if inherited := os.Getenv("TYCSWAP_DEFAULT_KEYCHAIN_SERVICE"); os.Getenv("TYCSWAP_DEFAULT_PROFILE_DIR") == defaultProfileDir && (inherited == "Claude Code-credentials" || validDefaultService(inherited)) {
		defaultKeychainService = inherited
	}
	if absolute, err := filepath.Abs(defaultProfileDir); err == nil {
		defaultProfileDir = absolute
	}

	// (3) no legacy-dir move any more (DESIGN Amendment A23): an old store
	// is only ever copied, by `tycswap migrate`, and never touched here.

	sequenceFile := filepath.Join(backupDir, "sequence.json")
	configsDir := filepath.Join(backupDir, "configs")
	credentialsDir := filepath.Join(backupDir, "credentials")
	lockFile := filepath.Join(backupDir, ".lock")

	// (5) lazy logging + usage store. logging.NewWithClock does NOT create the
	// directory until the first write, and usage.NewStore does not touch disk —
	// so a no-op run never materializes backupDir.
	log := logging.NewWithClock(backupDir, opts.Debug, clk)
	// Route oauth's paste-safe WARNING/DEBUG lines into this store's log; with two stores the last wins, which is benign (same root).
	oauth.Log = log
	usageStore := usage.NewStore(filepath.Join(backupDir, "cache"), clk)

	// (6) credential store — constructed BEFORE run_migrations because the macOS
	// migration performs storage ops through it.
	creds := credstore.New(credstore.Config{Platform: plat, CredentialsDir: credentialsDir}, kc, clk, log)

	s := &Store{
		Home:                   home,
		SequenceFile:           sequenceFile,
		ConfigsDir:             configsDir,
		CredentialsDir:         credentialsDir,
		LockFile:               lockFile,
		Platform:               plat,
		Creds:                  creds,
		Usage:                  usageStore,
		Lock:                   filelock.New(lockFile, 0),
		OAuth:                  opts.OAuth,
		Log:                    log,
		Clk:                    clk,
		backupDir:              backupDir,
		kc:                     kc,
		wc:                     wc,
		defaultProfileDir:      defaultProfileDir,
		defaultUnpinned:        defaultUnpinned,
		defaultKeychainService: defaultKeychainService,
	}

	usageStore.SetOnChange(s.PublishStatusline)

	// (7) registry migrations — self-contained, never abort construction. Every
	// error is logged (via the Host's Logger) and left for retry; only the
	// user-facing progress notices come back for relaying to stderr.
	for _, notice := range migrations.Run(migHost{s}) {
		fmt.Fprintln(stderr, notice)
	}

	return s, nil
}

// BackupDir is the tycswap backup root. It is a method, not a field, so
// *core.Switcher (which embeds *Store) can satisfy the frozen
// autoswitch.Switcher / tui.Facade BackupDir() interfaces (DESIGN A13).
func (s *Store) BackupDir() string { return s.backupDir }

// Keychain is the macOS Keychain client the store was built with
// (Options.Keychain; keychain.Security by default).
func (s *Store) Keychain() keychain.KeychainClient { return s.kc }

func (s *Store) timestamp() string {
	return s.Clk.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// migHost adapts *Store to migrations.Host (DESIGN A3), exposing exactly the
// narrow surface the two registry migrations touch. migrations never imports
// store; store constructs this adapter and passes it to migrations.Run.
type migHost struct{ s *Store }

func (h migHost) BackupDir() string      { return h.s.backupDir }
func (h migHost) CredentialsDir() string { return h.s.CredentialsDir }
func (h migHost) StateFilePath() string {
	return filepath.Join(h.s.backupDir, migrations.StateFilename)
}
func (h migHost) Platform() platform.Platform       { return h.s.Platform }
func (h migHost) Clock() clock.Clock                { return h.s.Clk }
func (h migHost) Logger() *logging.Logger           { return h.s.Log }
func (h migHost) Creds() credstore.Store            { return h.s.Creds }
func (h migHost) Keychain() keychain.KeychainClient { return h.s.kc }
func (h migHost) WinCred() wincred.Client           { return h.s.wc }

// SequenceAccounts returns the slot→email map from sequence.json and whether it
// was present and parseable — ok=false collapses "missing" and "corrupt" into
// one (both make _get_sequence_data return None), which both migrations treat
// identically (skip, never mark applied).
func (h migHost) SequenceAccounts() (map[string]string, bool) {
	data, _ := h.s.ReadSequence()
	if data == nil {
		return nil, false
	}
	out := make(map[string]string, len(data.Accounts))
	for num, raw := range data.Accounts {
		out[num] = strField(decodeRecord(raw), "email")
	}
	return out, true
}

var _ migrations.Host = migHost{}

// SetupDirectories creates the backup, configs, and credentials directories
// (parents included) and chmods each to 0700 on non-Windows (spec 01§1.3
// _setup_directories). It never creates sessions/ or cache/ (their owners
// create those lazily).
func (s *Store) SetupDirectories() error {
	for _, dir := range []string{s.backupDir, s.ConfigsDir, s.CredentialsDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if !platform.IsWindows() {
			if err := os.Chmod(dir, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}
