package switching

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"

	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// UsageProvider returns the decision-grade usage map keyed by account number,
// mirroring Python's _usage_by_account(): each value is a usage dict
// (map[string]any), a sentinel string, or nil. core wires it to
// reporting.UsageByAccount; a nil provider yields an empty map so the strategies
// see every account as "unknown" (never auto-skipped, best stays put).
var UsageProvider func(s *store.Store) map[string]any

// PostSwitchList renders the post-switch usage summary (the nested
// list_accounts() call in _perform_switch, run AFTER the locks release so its
// persist callbacks can re-acquire them). core wires it to reporting's list
// renderer; a nil hook skips the display (the switch itself has committed).
var PostSwitchList func(s *store.Store) error

var AutoAddCurrent func(s *store.Store) (activeNum string, err error)

// usageByAccount invokes the UsageProvider seam, returning an empty (non-nil)
// map when unwired so callers never nil-deref.
func usageByAccount(s *store.Store) map[string]any {
	if UsageProvider == nil {
		return map[string]any{}
	}
	if m := UsageProvider(s); m != nil {
		return m
	}
	return map[string]any{}
}

func headroomOf(v any, models []string) *float64 {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return oauth.AccountHeadroom(oauth.NewUsage(m), models)
}

func relevantWindowsOf(v any, models []string) []oauth.RelevantWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return oauth.RelevantWindows(oauth.NewUsage(m), models)
}

func isUsageDict(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

func numRef(numStr, email string) map[string]any {
	n, _ := strconv.Atoi(numStr)
	return jsonout.AccountRef(&n, email)
}

func nilNumRef(email string) map[string]any {
	return jsonout.AccountRef(nil, email)
}

// withTripleLock runs fn under the switch lock stack in the mandated order
// (spec 03§7.4): tycswap FileLock, then Claude Code credentials lock, then Claude
// Code config lock, then, innermost, Claude Code's storage-write lock, under
// which Claude Code read-modify-writes the credential store a switch rewrites
// (DESIGN A55) — released in reverse via defers. A FileLock timeout is a
// LockError; a Claude Code lock timeout is a ClaudeCodeLockTimeout. Nothing is
// mutated when acquisition fails. The FileLock is non-reentrant, so no network
// I/O may run inside fn.
func withTripleLock(s *store.Store, fn func() error) error {
	return withTripleLockTarget(s, "", fn)
}

func withTripleLockTarget(s *store.Store, target string, fn func() error) error {
	ok, err := s.Lock.Acquire(0)
	if err != nil {
		return err
	}
	if !ok {
		return cerr.Lock("Failed to acquire lock - another instance may be running")
	}
	defer s.Lock.Release()
	if s.GroupID() != "" {
		source, err := legacyHandoffProfile(s, target)
		if err != nil {
			return err
		}
		if source != "" {
			cred, err := cclock.AcquireProfileCredentials(source, 0, s.Clk)
			if err != nil {
				return err
			}
			defer cred.Release()
			config, err := cclock.Acquire(filepath.Join(source, ".claude.json")+".lock", 0, s.Clk)
			if err != nil {
				return err
			}
			defer config.Release()
			storage, err := cclock.AcquireProfileStorageWrite(source, 0, s.Clk)
			if err != nil {
				return err
			}
			defer storage.Release()
		}
	}
	if s.GroupID() != "" && defaultHandoffRequired(s, target) {
		cred, err := cclock.AcquireProfileCredentials(s.DefaultProfileDir(), 0, s.Clk)
		if err != nil {
			return err
		}
		defer cred.Release()
		config, err := cclock.Acquire(s.DefaultConfigPath()+".lock", 0, s.Clk)
		if err != nil {
			return err
		}
		defer config.Release()
		storage, err := cclock.AcquireProfileStorageWrite(s.DefaultProfileDir(), 0, s.Clk)
		if err != nil {
			return err
		}
		defer storage.Release()
	}

	acquireCred := func() (*cclock.Handle, error) { return cclock.AcquireCredentials(0, s.Clk) }
	acquireStorage := func() (*cclock.Handle, error) { return cclock.AcquireStorageWrite(0, s.Clk) }
	configLock := cclock.ConfigLockDir()
	if s.GroupID() != "" {
		acquireCred = func() (*cclock.Handle, error) { return cclock.AcquireProfileCredentials(s.ProfileDir(), 0, s.Clk) }
		acquireStorage = func() (*cclock.Handle, error) { return cclock.AcquireProfileStorageWrite(s.ProfileDir(), 0, s.Clk) }
		configLock = s.GlobalConfigPath() + ".lock"
	}
	credH, err := acquireCred()
	if err != nil {
		return err
	}
	defer credH.Release()

	cfgH, err := cclock.Acquire(configLock, 0, s.Clk)
	if err != nil {
		return err
	}
	defer cfgH.Release()

	storeH, err := acquireStorage()
	if err != nil {
		return err
	}
	defer storeH.Release()

	return fn()
}

func decodeRec(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

func recStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func accountRec(data *store.SequenceData, num string) (map[string]any, bool) {
	if data == nil {
		return nil, false
	}
	raw, ok := data.Accounts[num]
	if !ok {
		return nil, false
	}
	return decodeRec(raw), true
}

func disabledFromData(data *store.SequenceData, num string) bool {
	rec, ok := accountRec(data, num)
	if !ok {
		return false
	}
	d, _ := rec["disabled"].(bool)
	return d
}

// bgCtx is the context for the advisory pre-lock profile resolution (its own 5s
// timeout lives inside oauth.Profile).
func bgCtx() context.Context { return context.Background() }

func itoa(n int) string { return strconv.Itoa(n) }

func parseInt(s string) (int, error) { return strconv.Atoi(s) }

func storeTimestamp(s *store.Store) string {
	return s.Clk.Now().UTC().Format("2006-01-02T15:04:05Z")
}
