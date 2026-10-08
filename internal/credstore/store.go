// Package credstore routes credentials between the macOS Keychain and files: .enc backups, .prev retention and the unclaimed stash.
package credstore

import (
	"sync"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/platform"
)

const (
	// Distinct from the active-credential and old keyring services so migration items coexist.
	securityService = keychain.BackupService
	// claudeCodeKeychainService is Claude Code's active OAuth credential service.
	claudeCodeKeychainService = "Claude Code-credentials"
	// managedKeychainService is Claude Code's active managed-API-key service
	// (no -credentials suffix).
	managedKeychainService = "Claude Code"

	activeReadAttempts   = 2
	activeReadRetryDelay = 300 * time.Millisecond
	recheckCooldown      = 60 * time.Second
)

type Store interface {
	ReadActive() (value string, keychainUnavailable bool, err error)
	// WriteActive persists the active credential on a single auth axis: an OAuth
	// blob clears any managed key and vice-versa.
	WriteActive(creds string) error
	WriteActiveAccount(creds string) error
	// ReadLiveOAuth returns the live OAuth credential text as stored (the
	// Keychain item while in use, else the plaintext file), "" when there is
	// none. Unlike ReadActive it returns one holding seat-wide keys only.
	ReadLiveOAuth() string
	ClearActive() error

	// ReadBackup returns a slot's backup credential (.enc-wins), "" when missing;
	// it never fails (all backend errors are swallowed with a warning log).
	ReadBackup(num, email string) (string, error)
	WriteBackup(num, email, creds string) error
	// DeleteBackup is the best-effort sweep (legacy account-None alias, .prev,
	// quiet Keychain); it never fails.
	DeleteBackup(num, email string) error
	// DeleteBackupStrict is the fail-closed transactional clear: it propagates
	// backend errors as a CredentialError "aborting before commit".
	DeleteBackupStrict(num, email string) error

	ReadPrev(num, email string) (string, error)
	DeletePrev(num, email string) error

	KCReadBackup(num, email string) (string, error)
	KCWriteBackup(num, email, creds string) error

	// WriteUnclaimed stashes credential bytes of unknown provenance (entry file
	// written before the manifest) and returns the entry id.
	WriteUnclaimed(creds string, ctx map[string]any) (id string, err error)
	ListUnclaimed() (map[string]map[string]any, error)

	// LastActiveBackend reports where the most recent active-credential write
	// landed ("keychain" | "file" | "").
	LastActiveBackend() string
}

type Config struct {
	Platform       platform.Platform
	CredentialsDir string
}

type FileKeychainStore struct {
	platform       platform.Platform
	credentialsDir string
	kc             keychain.KeychainClient
	clk            clock.Clock
	log            *logging.Logger

	// Guarded by mu: the tri-state usability cache (nil = unprobed), the re-probe deadline and the last write's backend.
	mu                sync.Mutex
	cache             *bool
	disabledUntil     time.Time
	lastActiveBackend string
}

func New(cfg Config, kc keychain.KeychainClient, clk clock.Clock, log *logging.Logger) *FileKeychainStore {
	return &FileKeychainStore{
		platform:       cfg.Platform,
		credentialsDir: cfg.CredentialsDir,
		kc:             kc,
		clk:            clk,
		log:            log,
	}
}

var _ Store = (*FileKeychainStore)(nil)

func (s *FileKeychainStore) macOS() bool { return s.platform == platform.MacOS }

func (s *FileKeychainStore) useKeychain() bool {
	if !s.macOS() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && !*s.cache && !s.disabledUntil.IsZero() && !s.clk.Now().Before(s.disabledUntil) {
		s.cache = nil
		s.disabledUntil = time.Time{}
	}
	return s.cache == nil || *s.cache
}

func (s *FileKeychainStore) learn(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		// A secret too large for security's stdin is a property of the
		// payload, not of the Keychain: the caller stores it in a file, and the
		// Keychain stays in use for everything else.
		if keychain.IsUnusable(err) && !keychain.IsTooLarge(err) {
			f := false
			s.cache = &f
			s.disabledUntil = s.clk.Now().Add(recheckCooldown)
		}
		return err
	}
	if s.cache == nil {
		t := true
		s.cache = &t
	}
	return nil
}

// pinFileMode pins file mode for the rest of the process with no re-probe
// (spec 03§5.3): cache False, deadline cleared. Used after an active-credential
// write falls back to file, where a best-effort Keychain delete may have failed.
func (s *FileKeychainStore) pinFileMode() {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := false
	s.cache = &f
	s.disabledUntil = time.Time{}
}

// setBackend records where the last active-credential write landed.
func (s *FileKeychainStore) setBackend(b string) {
	s.mu.Lock()
	s.lastActiveBackend = b
	s.mu.Unlock()
}

// LastActiveBackend returns the most recent active-credential write backend.
func (s *FileKeychainStore) LastActiveBackend() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActiveBackend
}

// sleep blocks for d using the injected clock's Sleeper when it implements one
// (the Fake advances deterministically), else the real time.Sleep.
func (s *FileKeychainStore) sleep(d time.Duration) {
	if sl, ok := s.clk.(clock.Sleeper); ok {
		sl.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (s *FileKeychainStore) kcGet(service, account string) (string, bool, error) {
	v, found, err := s.kc.Get(service, account)
	if lerr := s.learn(err); lerr != nil {
		return "", false, lerr
	}
	return v, found, nil
}

func (s *FileKeychainStore) kcSet(service, account, password string) error {
	return s.learn(s.kc.Set(service, account, password))
}

func (s *FileKeychainStore) kcDelete(service, account string) error {
	return s.learn(s.kc.Delete(service, account))
}
