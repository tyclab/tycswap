package lifecycle

import (
	"sort"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/store"
)

func RemoveAccount(s *store.Store, identifier string, assumeYes bool) error {
	if !sequenceFileExists(s) {
		return cerr.Config("No accounts are managed yet")
	}

	data, err := s.MigratedSequenceForUpdate()
	if err != nil {
		return err
	}

	// Identifier gate: a non-digit must be a known alias or a format-valid email.
	if !isDigits(identifier) {
		isAlias := s.AliasInUse(data, identifier, "") != ""
		if !isAlias && !validateEmail(identifier) {
			return cerr.Validation("Invalid account identifier: %s", identifier)
		}
		if !isAlias {
			var matches []string
			for _, num := range sortedSlots(data) {
				if decodeRecord(data.Accounts[num]).str("email") == identifier {
					matches = append(matches, num)
				}
			}
			if len(matches) > 1 {
				emitLine("Multiple accounts found for '" + identifier + "':")
				for _, num := range matches {
					rec := decodeRecord(data.Accounts[num])
					tag := displayTag(rec.str("organizationName"))
					emitLine("  " + num + ": " + identifier + " " + printer.Muted("["+tag+"]"))
				}
				choice, ok := ActivePrompter.Prompt("Enter account number to remove: ")
				choice = trimSpace(choice)
				if !ok || !isDigits(choice) || !containsStr(matches, choice) {
					emitLine(printer.Dimmed("Cancelled"))
					return nil
				}
				identifier = choice
			}
		}
	}

	accountNum, email, org, err := s.ResolveAccount(identifier)
	if err != nil {
		return err
	}
	confirmedOrgName := ""
	if rec, ok := recordAt(data, accountNum); ok {
		confirmedOrgName = rec.str("organizationName")
	}

	if err := s.EnsureNoLiveSession(accountNum, email, "--remove-account"); err != nil {
		return err
	}

	active := "None"
	if data.ActiveAccountNumber != nil {
		active = strconv.Itoa(*data.ActiveAccountNumber)
	}
	if active == accountNum {
		emitWarning("Warning: Account-" + accountNum + " (" + email + ") is currently active")
	}

	if !assumeYes {
		confirm, ok := ActivePrompter.Prompt("Are you sure you want to permanently remove Account-" + accountNum + " (" + email + ")? [y/N] ")
		if !ok || strings.ToLower(confirm) != "y" {
			emitLine(printer.Dimmed("Cancelled"))
			return nil
		}
	}

	// The commit span. An unbounded amount of time passed at the prompt with
	// nothing yet destroyed, so the roster the deletion is applied to is read
	// HERE, under the lock that commits it — not the one the question was asked
	// against — and the slot is re-checked against the identity the user
	// confirmed before a single file is deleted. This is a classified read, not a
	// fallback: a file that has gone unparseable refuses while every backup is
	// still intact, and a file the user deleted meanwhile (the refusal message's
	// own second remedy) reads as the fresh roster it is, so this call cannot
	// resurrect the records they discarded.
	removed := false
	if err := s.WithRosterLocked(func(data *store.SequenceData) error {
		rec, present := recordAt(data, accountNum)
		if !present {
			if moved := s.FindAccountSlot(data, email, org); moved != "" {
				return cerr.Config(
					"Account-%s (%s) moved to slot %s while the confirmation was open, so nothing was removed. Re-run the command against slot %s to remove it there.",
					accountNum, email, moved, moved)
			}
			emitLine(printer.Dimmed("Account-" + accountNum + " (" + email + ") was already removed by another tycswap; nothing to do"))
			return nil
		}
		// The identity the user confirmed is the composite (email,
		// organizationUuid) the store matches on everywhere — the same key the
		// absent-slot branch above searches by. Two managed accounts sharing an
		// email in different orgs is a supported shape, so the email alone is not a
		// name for an account: a concurrent renumbering that moves the OTHER
		// same-email record into this slot leaves the email identical, and deleting
		// on that evidence destroys an account the user was never shown.
		if now, nowOrg := rec.str("email"), rec.str("organizationUuid"); now != email || nowOrg != org {
			return cerr.Config(
				"Slot %s changed while the confirmation was open: it now holds %s, not %s. Nothing was changed — re-run the command to remove the account you meant.",
				accountNum,
				taggedIdentity(now, rec.str("organizationName")),
				taggedIdentity(email, confirmedOrgName))
		}
		// Re-checked inside the lock: a session that went live during the prompt
		// must still stop the delete (DeleteAccountFiles re-checks as a safety
		// net, but this is the refusal with the actionable message).
		if err := s.EnsureNoLiveSession(accountNum, email, "--remove-account"); err != nil {
			return err
		}
		if err := s.DeleteAccountFiles(accountNum, email); err != nil {
			return err
		}
		delete(data.Accounts, accountNum)
		if n, ok := parseSlot(accountNum); ok {
			data.Sequence = removeInt(data.Sequence, n)
		}
		data.LastUpdated = timestamp(s)
		if err := s.WriteSequence(data); err != nil {
			return err
		}
		removed = true
		if s.Log != nil {
			s.Log.Infof("Removed account %s: %s", accountNum, email)
		}
		emitLine(printer.Accent("Removed") + " Account-" + accountNum + " (" + email + ")")
		return nil
	}); err != nil {
		return err
	}

	// mappings.json is a different file with its own consistency; pruning it
	// needs no lock, and only a removal this call actually performed has
	// mappings to retire.
	if removed {
		pruneMappings(s, email, org)
	}
	return nil
}

// taggedIdentity renders an account the way the disambiguation list does —
// email plus org tag — for the messages that must distinguish two accounts
// sharing an email. An account with no org reads as [personal], never as a bare
// email, so the two sides of a comparison are always in the same shape.
func taggedIdentity(email, orgName string) string {
	return email + " [" + displayTag(orgName) + "]"
}

func sortedSlots(data *store.SequenceData) []string {
	keys := make([]string, 0, len(data.Accounts))
	for k := range data.Accounts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ni, oki := parseSlot(keys[i])
		nj, okj := parseSlot(keys[j])
		if oki && okj {
			return ni < nj
		}
		return keys[i] < keys[j]
	})
	return keys
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
