package reporting

import (
	"strconv"

	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

// BuildAccountsInfo builds the ordered per-account info rows (spec 02§13). Rows
// follow sequence order. The active slot is the live login's slot
// (store.ResolveLiveSlot); its credential and the OAuth-Keychain-unavailable
// flag come from the active store, every other slot's from its backup. Absence
// of a live login or a managed match leaves all rows inactive.
func BuildAccountsInfo(s *store.Store) []AccountInfo {
	data, _ := s.SequenceMigrated()
	if data == nil {
		return nil
	}

	activeNum := s.ResolveLiveSlot(data).Slot

	infos := make([]AccountInfo, 0, len(data.Sequence))
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		rec, _ := recordFor(data, num)

		// account.get("email", "unknown"): absent is "unknown", present but non-string is "".
		email := "unknown"
		if v, present := rec["email"]; present {
			email, _ = v.(string)
		}
		orgName := recStr(rec, "organizationName")
		orgUUID := recStr(rec, "organizationUuid")
		alias := recStr(rec, "alias")

		isActive := activeNum != "" && num == activeNum
		creds := ""
		keychainUnavailable := false
		if isActive {
			// active.value or "" — a genuine read error degrades to "" for
			// display, matching Python's _read_active_credentials contract.
			val, kcUnavail, _ := s.Creds.ReadActive()
			creds = val
			keychainUnavailable = kcUnavail
			if s.GroupID() == "" && paths.GetClaudeConfigHome() != s.ProfileDir() {
				creds, _ = s.ReadOwnedCredentials(num, email)
				keychainUnavailable = false
			}
		} else {
			creds, _ = s.ReadAccountCredentials(num, email)
			if owner, err := s.CredentialOwner(num); err != nil {
				creds = ""
			} else if owner.Scope != "" {
				creds, _ = s.ReadOwnedCredentials(num, email)
			}
		}

		infos = append(infos, AccountInfo{
			Number:              n,
			Email:               email,
			OrgName:             orgName,
			OrgUUID:             orgUUID,
			IsActive:            isActive,
			Creds:               creds,
			Alias:               alias,
			KeychainUnavailable: keychainUnavailable,
			BaseURL:             store.BaseURLFrom(data, num),
		})
	}
	return infos
}
