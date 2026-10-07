package migrations

import (
	"github.com/tyclab/tycswap/internal/logging"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/slotkey"
)

type relocateConfig struct {
	// label prefixes every warning this relocation logs (matches Python's
	// f"{context}: ..." messages, e.g. "windows_keyring_to_files").
	label string
	// pending is the slot-number → email map to actually process this call
	// (the macOS migration pre-filters this to slots not yet in the security
	// service; the Windows migration passes every managed account).
	pending map[string]string
	// allAccounts is every managed account (not just pending), used to
	// compute the email-uniqueness count the account-None fallback is gated
	// on — always the full set, even when pending is a strict subset (spec
	// 07§5.4's pre-check narrowing must not change who "unique email" means).
	allAccounts map[string]string

	readLegacy func(username string) (string, error)
	// deleteLegacy best-effort removes one legacy username; it never returns
	// an error — failures are logged internally (mirrors
	// _delete_keyring_quietly's swallow-and-warn).
	deleteLegacy func(username string)

	writeNew func(num, email, creds string) error
	readNew  func(num, email string) (string, error)
	// deleteBadNew best-effort discards a just-written new-backend entry that
	// failed verification, so it can never shadow the still-intact legacy
	// source on a retry.
	deleteBadNew func(num, email string)

	afterSuccess func(num, email, sourceUsername string)

	log *logging.Logger
}

func relocate(cfg relocateConfig) (migrated, failed int) {
	emailCounts := map[string]int{}
	for _, email := range cfg.allAccounts {
		emailCounts[email]++
	}

	for _, num := range sortedSlotKeys(cfg.pending) {
		email := cfg.pending[num]
		canonical := "account-" + num + "-" + email
		noneUser := "account-None-" + email

		creds, err := cfg.readLegacy(canonical)
		if err != nil {
			cfg.log.Warningf("%s: read of %s failed: %v", cfg.label, canonical, err)
			failed++
			continue
		}

		sourceUsername := canonical
		if creds == "" && num != "None" && emailCounts[email] == 1 {
			creds, err = cfg.readLegacy(noneUser)
			if err != nil {
				cfg.log.Warningf("%s: read of %s failed: %v", cfg.label, noneUser, err)
				failed++
				continue
			}
			if creds != "" {
				sourceUsername = noneUser
			}
		}

		if creds == "" {
			continue
		}

		// A slot holds the account part only (DESIGN A29): the old tool
		// captured the live credential whole, MCP server logins and client
		// secrets included, and those are the seat's. The read-back is
		// compared with what is written.
		creds = oauth.AccountOnly(creds)

		if err := cfg.writeNew(num, email, creds); err != nil {
			cfg.log.Warningf("%s: write/read-back for %s failed: %v", cfg.label, canonical, err)
			cfg.deleteBadNew(num, email)
			failed++
			continue
		}
		readback, err := cfg.readNew(num, email)
		if err != nil {
			cfg.log.Warningf("%s: write/read-back for %s failed: %v", cfg.label, canonical, err)
			cfg.deleteBadNew(num, email)
			failed++
			continue
		}
		if readback != creds {
			cfg.log.Warningf("%s: read-back mismatch for %s; discarding the bad copy and leaving the legacy entry in place", cfg.label, canonical)
			cfg.deleteBadNew(num, email)
			failed++
			continue
		}

		cfg.deleteLegacy(sourceUsername)
		if num != "None" && sourceUsername != noneUser {
			cfg.deleteLegacy(noneUser)
		}
		if cfg.afterSuccess != nil {
			cfg.afterSuccess(num, email, sourceUsername)
		}
		migrated++
	}
	return migrated, failed
}

func sortedSlotKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return slotkey.Sorted(keys)
}
