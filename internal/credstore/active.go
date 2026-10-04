// The active credential (Claude Code's own store): the ordered OAuth-then-
// managed-key read with the bounded Keychain retry, and the single-axis write
// that clears the opposite axis. macOS routes through the Keychain while usable;
// every other platform uses the plaintext .credentials.json / primaryApiKey.
//
// Implements spec 03§5.4–5.6 (reading/writing the active credential and the
// key-scoped ~/.claude.json RMW), delegating the file I/O to internal/ccfile.

package credstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
)

// ReadActive reads Claude Code's active credential, classifying the outcome
// (spec 03§5.4). It tries the OAuth credential fully first (Keychain then the
// plaintext file) so a Keychain-empty OAuth-file login is never misread as an
// API key, then the managed-key locations. A present-but-unreadable plaintext
// credentials file returns a non-nil error (Python's None outcome, which the
// caller maps to a CredentialReadError); "nothing anywhere" returns "" with the
// keychainUnavailable flag set when the OAuth Keychain read failed uncovered.
//
// An OAuth Keychain item or file holding nothing but seat-wide keys
// (ccfile.SeatWideOnly: the MCP server logins and client secrets Claude Code
// writes on an API-key seat) is no login and reads as absent, so the managed
// key behind it is found (DESIGN A29). Every "is there a live login, and
// which" decision reads through here, so they all agree: the switch-time
// backup and classifier, the self-switch check, the direct-activation stash,
// add, export, status, list, the usage fetch and session bootstrap.
func (s *FileKeychainStore) ReadActive() (string, bool, error) {
	keychainFailed := false
	// 1. OAuth Keychain (macOS, when usable), with a bounded retry.
	if s.useKeychain() {
		val, failed := s.readActiveOAuthKeychain()
		keychainFailed = failed
		if val != "" && !ccfile.SeatWideOnly(val) {
			return val, false, nil
		}
	} else if s.macOS() {
		// Keychain already known unusable this process: absence below is
		// "keychain unavailable", not a genuinely empty slot.
		keychainFailed = true
	}

	// 2. OAuth plaintext file (Claude Code's own fallback; every platform).
	raw, existsFile, rerr := ccfile.ReadCredentialsFile()
	if rerr != nil {
		// A present credentials file that could not be read → Python's None.
		s.log.Errorf("Failed to read credentials file: %v", rerr)
		return "", false, rerr
	}
	if existsFile && strings.TrimSpace(raw) != "" && !ccfile.SeatWideOnly(raw) {
		return raw, false, nil // raw text, NOT stripped
	}

	// 3. Managed API key.
	if key := s.readManagedKey(); key != "" {
		return key, false, nil
	}
	return "", keychainFailed, nil
}

// readActiveOAuthKeychain reads the active OAuth Keychain item with a bounded
// retry (spec 03§5.4). Returns (value, failed): value is "" when absent (rc-44,
// not retried) or after every attempt failed; failed is true only when every
// attempt raised.
func (s *FileKeychainStore) readActiveOAuthKeychain() (string, bool) {
	var lastErr error
	for attempt := 0; attempt < activeReadAttempts; attempt++ {
		v, _, err := s.kcGet(claudeCodeKeychainService, keychain.AccountName())
		if err == nil {
			return v, false
		}
		lastErr = err
		if attempt+1 < activeReadAttempts {
			s.sleep(activeReadRetryDelay)
		}
	}
	s.log.Warningf("Keychain read failed after %d attempt(s), trying file: %v", activeReadAttempts, lastErr)
	return "", true
}

// readManagedKey reads the active managed API key, "" when absent (spec 03§5.4):
// macOS Keychain "Claude Code" first, then ~/.claude.json primaryApiKey.
func (s *FileKeychainStore) readManagedKey() string {
	if s.useKeychain() {
		v, _, err := s.kcGet(managedKeychainService, keychain.AccountName())
		if err != nil {
			s.log.Warningf("Managed-key Keychain read failed: %v", err)
		} else if v != "" {
			return v
		}
	}
	if cfg := s.readGlobalConfig(); cfg != nil {
		if k, ok := cfg["primaryApiKey"].(string); ok && k != "" {
			return k
		}
	}
	return ""
}

// readGlobalConfig is the lenient ~/.claude.json read (spec 03§5.4): absent or
// unreadable → nil (with a warning on a genuine error, mirroring Python's
// swallow-to-None). A non-object top level also reads as nil (ccfile surfaces it
// as an error, so it is logged here — a log-only difference from Python).
func (s *FileKeychainStore) readGlobalConfig() map[string]any {
	m, err := ccfile.ReadGlobalConfig()
	if err != nil {
		s.log.Warningf("Failed to read global config: %v", err)
		return nil
	}
	return m
}

// WriteActive writes Claude Code's active credential, enforcing a single auth
// axis (spec 03§5.5): a managed key clears OAuth and vice-versa.
func (s *FileKeychainStore) WriteActive(creds string) error {
	if LooksLikeAPIKey(creds) {
		return s.writeManagedCredentials(strings.TrimSpace(creds), false)
	}
	if err := s.writeOAuthCredentials(creds); err != nil {
		return err
	}
	s.clearManagedKey()
	return nil
}

// WriteActiveAccount writes a stored account credential as Claude Code's active
// one, carrying the live credential's seat-wide remainder — the MCP server
// logins and client secrets under ccfile.SeatWideKeys — over it (DESIGN A25
// item 9, A29). It reads the live credential first (the Keychain while it is
// in use, else the plaintext file) and splices with ccfile.SpliceCredentials,
// which drops the stored blob's own copy of those keys; a failed read or a
// live file that does not parse writes the stored account part without a
// carry-over, with a warning: the carry-over is best-effort and never stops a
// switch. An API key takes WriteActive's managed path, except that a failure
// after the key reached the Keychain puts the Keychain item back. WriteActive
// itself stays verbatim: it is what rollback restores with, and what the
// refresh write-back uses for a blob that already holds the live remainder.
func (s *FileKeychainStore) WriteActiveAccount(creds string) error {
	if LooksLikeAPIKey(creds) {
		return s.writeManagedCredentials(strings.TrimSpace(creds), true)
	}
	merged, err := ccfile.SpliceCredentials(creds, s.readLiveOAuth())
	if err != nil {
		s.log.Warningf("Writing the stored credential without the live MCP server logins; they could not be carried over it: %v", err)
	}
	return s.WriteActive(merged)
}

// ClearActive takes every login off Claude Code's credential store for an
// account that authenticates through Claude Code's settings.json instead: an
// API-key account with a base URL, whose key goes there as
// env.ANTHROPIC_AUTH_TOKEN (DESIGN A46). The managed key is removed (the
// Keychain item and primaryApiKey, as an OAuth write removes it) and the
// OAuth login is cleared the way a switch onto a key clears it, keeping its
// seat-wide part, the MCP server logins and client secrets (DESIGN A29).
// Nothing is stored in their place, so ReadActive finds no credential. A
// rollback onto a state that held no credential at all restores it with this
// as well.
func (s *FileKeychainStore) ClearActive() error {
	if err := s.clearOAuthLogin(func() error { return nil }); err != nil {
		return err
	}
	s.clearManagedKey()
	return nil
}

// readLiveOAuth returns the live OAuth credential text for the carry-over: the
// Keychain item while the Keychain is in use (bounded retry, then the file),
// else the plaintext file; "" when neither holds one or the file read fails
// (logged, so the caller writes verbatim). Unlike ReadActive it returns a
// value holding seat-wide keys only, deliberately: that value is no login, but
// it is exactly the remainder a switch carries over (DESIGN A29).
func (s *FileKeychainStore) readLiveOAuth() string {
	if s.useKeychain() {
		if v, _ := s.readActiveOAuthKeychain(); v != "" {
			return v
		}
	}
	raw, _, err := ccfile.ReadCredentialsFile()
	if err != nil {
		s.log.Warningf("Could not read the live credentials file; the live MCP server logins will not be carried over: %v", err)
		return ""
	}
	return raw
}

// ReadLiveOAuth is readLiveOAuth for a caller that must restore the live
// credential exactly, a seat-wide-only one included (DESIGN A30).
func (s *FileKeychainStore) ReadLiveOAuth() string { return s.readLiveOAuth() }

// writeOAuthCredentials writes Claude Code's active OAuth credential (spec
// 03§5.5). macOS writes the Keychain when usable and bumps an already-present
// shadow .credentials.json (#86 hot-reload); on failure or off macOS it writes
// the plaintext file, best-effort clears any stale Keychain entry, and pins file
// mode.
func (s *FileKeychainStore) writeOAuthCredentials(creds string) error {
	return s.writeOAuthCredentialsBeforeFile(creds, nil)
}

// writeOAuthCredentialsBeforeFile lets a managed-key write persist its file
// fallback before the OAuth remainder replaces the live login and pins file
// mode. Ordinary OAuth writes have no prerequisite.
func (s *FileKeychainStore) writeOAuthCredentialsBeforeFile(creds string, beforeFile func() error) error {
	if s.useKeychain() {
		err := s.kcSet(claudeCodeKeychainService, keychain.AccountName(), creds)
		if err == nil {
			s.refreshStaleCredentialsFile(creds)
			s.setBackend("keychain")
			return nil
		}
		if !keychain.IsUnusable(err) {
			return err // a programming error propagates
		}
		s.log.Warningf("Keychain write failed, falling back to file: %v", err)
	}
	// File mode: non-macOS, Keychain known unusable, or a just-failed write.
	if beforeFile != nil {
		if err := beforeFile(); err != nil {
			return err
		}
	}
	if err := ccfile.WriteCredentialsFile(creds); err != nil {
		return cerr.CredentialWrite("Failed to write credentials: %v", err)
	}
	s.deleteActiveKeychainEntry()
	if s.macOS() {
		s.pinFileMode()
	}
	s.setBackend("file")
	return nil
}

// refreshStaleCredentialsFile bumps an already-present .credentials.json's mtime
// after a Keychain write (rewrite-when-present / never-create, #86). Best-effort.
func (s *FileKeychainStore) refreshStaleCredentialsFile(creds string) {
	if !exists(paths.GetCredentialsPath()) {
		return
	}
	if err := ccfile.WriteCredentialsFile(creds); err != nil {
		s.log.Warningf("Could not refresh .credentials.json after Keychain write (%v); a running session may not hot-reload until restart", err)
	}
}

// writeManagedCredentials activates a managed API key, then clears OAuth (spec
// 03§5.6). It records the approved form on every platform (even on Keychain
// success) and stores the key in the Keychain when usable, else primaryApiKey.
//
// With undo (a switch's own write, WriteActiveAccount) an error leaves nothing
// for the caller's rollback, which runs only after a write that succeeded: on
// the file backend the key and its approval are one config write, and a
// failure after the Keychain write puts the managed Keychain item back
// (DESIGN A29). Without it (WriteActive: the rollback itself) a key stored
// before a later failure stays: it is the key the rollback restores.
func (s *FileKeychainStore) writeManagedCredentials(apiKey string, undo bool) error {
	wroteToKeychain := false
	var restoreKeychain func(dropped string) error
	if s.useKeychain() {
		var prev string
		var hadPrev bool
		var err error
		failed := "write"
		if undo {
			// What the item holds now is what a failure below puts back.
			if prev, hadPrev, err = s.readManagedKeychainItem(); err != nil {
				failed = "read"
			}
		}
		if err == nil {
			err = s.kcSet(managedKeychainService, keychain.AccountName(), apiKey)
		}
		if err == nil {
			wroteToKeychain = true
			if undo {
				restoreKeychain = func(dropped string) error {
					return s.restoreManagedKeychainItem(prev, hadPrev, dropped)
				}
			}
		} else if !keychain.IsUnusable(err) {
			return err // a programming error propagates
		} else {
			s.log.Warningf("Managed-key Keychain %s failed, falling back to config: %v", failed, err)
		}
	}

	approved := ApprovedForm(apiKey)
	dropped := "" // the primaryApiKey the Keychain write's config update removes
	mutate := func(cfg map[string]any) {
		responses, ok := cfg["customApiKeyResponses"].(map[string]any)
		if !ok {
			responses = map[string]any{}
		}
		approvedList, ok := responses["approved"].([]any)
		if !ok {
			approvedList = []any{}
		}
		found := false
		for _, v := range approvedList {
			if str, _ := v.(string); str == approved {
				found = true
				break
			}
		}
		if !found {
			approvedList = append(approvedList, approved)
		}
		responses["approved"] = approvedList
		if _, ok := responses["rejected"]; !ok {
			responses["rejected"] = []any{}
		}
		cfg["customApiKeyResponses"] = responses
		if wroteToKeychain {
			dropped, _ = cfg["primaryApiKey"].(string)
			delete(cfg, "primaryApiKey") // keep the key out of plaintext
		} else {
			cfg["primaryApiKey"] = apiKey
		}
	}
	if err := ccfile.UpdateGlobalConfig(mutate); err != nil {
		// Nothing reached the config, so only the Keychain item goes back.
		return managedWriteFailed(cerr.CredentialWrite("Failed to write managed API key: %v", err), restoreKeychain, "")
	}

	// Mutual exclusion: drop the OAuth login so it can't shadow the key. The
	// seat's MCP server logins and client secrets are no login and stay.
	if err := s.clearOAuthLogin(func() error {
		if wroteToKeychain {
			// Keeping the remainder can pin file mode (for example, MCP
			// tokens exceeding security's stdin limit). Reads then skip
			// the managed Keychain key too. Persist its fallback BEFORE
			// replacing the original OAuth login, so a failure leaves that
			// login intact and does not need a credential rollback.
			wroteToKeychain = false
			if err := ccfile.UpdateGlobalConfig(mutate); err != nil {
				return cerr.CredentialWrite("Failed to write managed API key for OAuth file fallback: %v", err)
			}
		}
		return nil
	}); err != nil {
		// The first config update landed: a previous key it dropped from
		// primaryApiKey is put back too.
		return managedWriteFailed(err, restoreKeychain, dropped)
	}
	if s.macOS() && !wroteToKeychain {
		// The key fell back to primaryApiKey while a stale "Claude Code" Keychain
		// item may remain (read before primaryApiKey). Pin so a cooldown re-probe
		// can't read that residual over the fresh fallback value.
		s.pinFileMode()
	}
	if wroteToKeychain {
		s.setBackend("keychain")
	} else {
		s.setBackend("file")
	}
	return nil
}

// readManagedKeychainItem reads the managed-key Keychain item with the active
// read's bounded retry (spec 03§5.4): its value and whether it exists, or the
// last error once every attempt failed. Only the outcome of the last attempt
// reaches the usability cache, so a failure the retry overcomes does not turn
// the Keychain off for the rest of the write.
func (s *FileKeychainStore) readManagedKeychainItem() (string, bool, error) {
	var err error
	for attempt := 0; attempt < activeReadAttempts; attempt++ {
		var v string
		var found bool
		if v, found, err = s.kc.Get(managedKeychainService, keychain.AccountName()); err == nil {
			return v, found, s.learn(nil)
		}
		if attempt+1 < activeReadAttempts {
			s.sleep(activeReadRetryDelay)
		}
	}
	return "", false, s.learn(err)
}

// restoreManagedKeychainItem puts the managed-key Keychain item back after a
// switch's write failed past storing its key there: the previous value; with
// no previous item, the previous key the failed write dropped from
// primaryApiKey, since the item is read first (by Claude Code too); else no
// item. Not through the usability cache, like clearManagedKey. Its error says
// the new key is still in the Keychain.
func (s *FileKeychainStore) restoreManagedKeychainItem(prev string, hadPrev bool, dropped string) error {
	var err error
	switch {
	case hadPrev:
		err = s.kc.Set(managedKeychainService, keychain.AccountName(), prev)
	case dropped != "":
		err = s.kc.Set(managedKeychainService, keychain.AccountName(), dropped)
	default:
		err = s.kc.Delete(managedKeychainService, keychain.AccountName())
	}
	if err != nil {
		s.log.Warningf("Could not put the managed-key Keychain item back after the failed write: %v", err)
		return fmt.Errorf("the new API key is still in the Keychain, putting the item back failed: %w", err)
	}
	return nil
}

// managedWriteFailed returns a failed managed-key write's error after putting
// the Keychain item back (restore is nil when there is nothing to undo). A
// restore that fails as well is joined to the error and appended to its
// message.
func managedWriteFailed(err error, restore func(dropped string) error, dropped string) error {
	if restore == nil {
		return err
	}
	rerr := restore(dropped)
	if rerr == nil {
		return err
	}
	return cerr.CredentialWrite("%v; %v", err, rerr).Wrap(errors.Join(err, rerr))
}

// clearManagedKey clears any active managed API key (Claude Code removeApiKey
// semantics, spec 03§5.6): deletes the macOS Keychain item (best-effort, not
// through the usability cache) and drops primaryApiKey, leaving
// customApiKeyResponses.approved intact. A no-op when no key is present.
func (s *FileKeychainStore) clearManagedKey() {
	if s.macOS() {
		_ = s.kc.Delete(managedKeychainService, keychain.AccountName())
	}
	cfg := s.readGlobalConfig()
	if cfg != nil {
		if v, ok := cfg["primaryApiKey"]; ok && v != nil {
			if err := ccfile.UpdateGlobalConfig(func(c map[string]any) { delete(c, "primaryApiKey") }); err != nil {
				s.log.Warningf("Failed to clear primaryApiKey: %v", err)
			}
		}
	}
}

// clearOAuthLogin clears the active OAuth login for a managed key and writes
// the live credential's seat-wide part back in its place (DESIGN A29), on the
// Keychain item and its shadow file or on the plaintext file, as any OAuth
// write lands. Claude Code's own move onto an API key drops claudeAiOauth and
// keeps the MCP server logins, and a credential holding seat-wide keys only is
// no login (ccfile.SeatWideOnly), so it cannot shadow the key. Without a
// seat-wide part, or when it cannot be written, the credential is cleared
// whole (spec 03§5.6). beforeFile makes the managed key readable in file mode
// before that fallback overwrites the original OAuth credential; its failure
// propagates without clearing the original login.
func (s *FileKeychainStore) clearOAuthLogin(beforeFile func() error) error {
	if rest, ok := ccfile.SeatWidePart(s.readLiveOAuth()); ok {
		var fallbackErr error
		err := s.writeOAuthCredentialsBeforeFile(rest, func() error {
			fallbackErr = beforeFile()
			return fallbackErr
		})
		if fallbackErr != nil {
			return fallbackErr
		}
		if err == nil {
			return nil
		}
		s.log.Warningf("Could not keep the MCP server logins beside the API key; clearing them with the OAuth login: %v", err)
	}
	// Reading the old OAuth item can disable the Keychain too, even when
	// no seat-wide remainder is found. Commit the managed fallback before
	// clearing that original login in this case as well.
	if !s.useKeychain() {
		if err := beforeFile(); err != nil {
			return err
		}
	}
	s.clearOAuthCredential()
	return nil
}

// clearOAuthCredential clears the active OAuth credential — Keychain item and
// plaintext file (best-effort, spec 03§5.6).
func (s *FileKeychainStore) clearOAuthCredential() {
	s.deleteActiveKeychainEntry()
	p := paths.GetCredentialsPath()
	if exists(p) {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Warningf("Failed to remove credentials file: %v", err)
		}
	}
}

// deleteActiveKeychainEntry best-effort removes the active OAuth Keychain item
// (macOS only, not through the usability cache) so Claude Code's Keychain-first
// read can't resurrect a stale entry after a file fallback (#30337).
func (s *FileKeychainStore) deleteActiveKeychainEntry() {
	if !s.macOS() {
		return
	}
	_ = s.kc.Delete(claudeCodeKeychainService, keychain.AccountName())
}
