// perform.go — _perform_switch (spec 02§8): the physical switch under the triple
// lock, with the issue-#117 outgoing-credential backup classification, the
// SwitchTransaction rollback ledger (normal path) and the inline
// direct-activation rollback, and the network-safe post-switch display.
//
// Locking (03§7.4, DESIGN §4): the identity prefetch (possibly network) runs
// BEFORE the locks; everything under withTripleLock is local I/O; the nested
// post-switch usage display runs AFTER the locks release so its persist
// callbacks can re-acquire the non-reentrant FileLock.
package switching

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/store"
)

// performSwitch performs the actual switch (spec 02§8). emitOutput=false (JSON
// mode) suppresses all human prints; the live-session warning rides back in
// the op's Warnings. forceActivate routes through the direct-activation path
// even with a managed live login. prov may be nil (resolved here).
func performSwitch(s *store.Store, targetAccount string, emitOutput, forceActivate bool, prov *Provenance) (switchOp, error) {
	warningsOut := []string{}

	// Session-mode drift warning (warn, never block).
	preData, _ := s.ReadSequence()
	preRec, _ := accountRec(preData, targetAccount)
	preEmail := recStr(preRec, "email")
	if preEmail != "" {
		if pids := s.LiveSessionPidsFor(targetAccount, preEmail); len(pids) > 0 {
			msg := "Account-" + targetAccount + " (" + preEmail + ") has a live session-mode " +
				"Claude instance (PID " + joinInts(pids) + "). Running the same account as both " +
				"the default login and a session can make one copy's token go stale if the server " +
				"rotates it. If the session later fails to authenticate, exit it and re-run " +
				"'tycswap run " + targetAccount + "'."
			if emitOutput {
				printWarning(msg)
			} else {
				warningsOut = append(warningsOut, msg)
			}
		}
	}

	// Pre-lock identity resolution (may hit the network — before the locks).
	if prov == nil {
		if forceActivate {
			prov = &Provenance{}
		} else {
			prov = prefetchLiveIdentity(s)
		}
	}

	var op switchOp
	var targetEmail, targetOrg string
	var plan *profilePlan
	displayAfterLock := false

	err := withTripleLock(s, func() error {
		data, err := s.ReadSequence()
		if err != nil {
			return err
		}
		currentAccount := ""
		if data.ActiveAccountNumber != nil {
			currentAccount = itoa(*data.ActiveAccountNumber)
		}
		tRec, ok := accountRec(data, targetAccount)
		if !ok {
			return cerr.AccountNotFound("Account-%s does not exist", targetAccount)
		}
		targetEmail = recStr(tRec, "email")
		targetOrg = recStr(tRec, "organizationUuid")
		toRef := numRef(targetAccount, targetEmail)

		curEmail, curOrg, curOK := s.GetCurrentAccount()
		_ = curOrg
		if curOK {
			currentAccount = s.FindAccountSlot(data, curEmail, curOrg)
		}

		// Direct-activation path.
		if forceActivate || !curOK || currentAccount == "" {
			var fromRef map[string]any
			switch {
			case !curOK:
				fromRef = nil
			case currentAccount == "":
				fromRef = nilNumRef(curEmail)
			default:
				fromRef = numRef(currentAccount, curEmail)
			}
			o, derr := directActivate(s, data, targetAccount, targetEmail, targetOrg, fromRef, toRef, curOK, curEmail, currentAccount, forceActivate, emitOutput, warningsOut)
			if derr != nil {
				return derr
			}
			op = o
			return nil
		}

		// Normal switch path (a managed live login exists).
		fromRef := numRef(currentAccount, curEmail)
		originalCreds, kcUnavailable, readErr := s.Creds.ReadActive()
		if readErr != nil {
			return cerr.CredentialRead("Failed to read current credentials")
		}
		// An account with a base URL keeps no credential in Claude Code's
		// store: its key is in settings.json (DESIGN A46), so an empty read
		// is its normal state, not a Keychain that did not answer. Its
		// record may have lost the URL since (a refresh without one), so the
		// live profile counts too. A Keychain that did not answer is still
		// refused: the store may hold a login the rollback could not restore.
		curEndpoint := store.BaseURLFrom(data, currentAccount) != "" || (originalCreds == "" && endpointIsLive(s))
		if originalCreds == "" && (!curEndpoint || kcUnavailable) {
			return cerr.CredentialRead("Current account credential is empty (Keychain unreadable?); refusing to overwrite its backup")
		}
		cfgText, cfgExists, cfgErr := readConfigText()
		if cfgErr != nil {
			return configReadError(cfgErr)
		}
		if !cfgExists {
			return cerr.Config("Claude config file not found")
		}
		originalConfig := cfgText

		tx := &switchTransaction{
			originalCredentials: originalCreds,
			originalConfig:      originalConfig,
			originalAccountNum:  currentAccount,
			originalEmail:       curEmail,
		}
		if originalCreds == "" {
			// Nothing but a seat-wide part (or nothing at all) is in the
			// store; that is what a rollback puts back.
			tx.originalCredentials = s.Creds.ReadLiveOAuth()
			tx.restoreClear = tx.originalCredentials == ""
		}

		commitErr := normalSwitchBody(s, data, tx, targetAccount, targetEmail, currentAccount, curEmail, originalCreds, originalConfig, curEndpoint, prov, emitOutput, &warningsOut)
		plan = tx.plan
		if commitErr != nil {
			if s.Log != nil {
				s.Log.Errorf("Switch failed: %v, attempting rollback", commitErr)
			}
			if len(tx.completedSteps) > 0 {
				if tx.rollback(s) {
					if s.Log != nil {
						s.Log.Infof("Rollback successful")
					}
					return cerr.Switch("Switch failed and was rolled back: %v", commitErr)
				}
				if s.Log != nil {
					s.Log.Errorf("Rollback failed!")
				}
				return cerr.Switch("Switch failed and rollback also failed: %v. Manual recovery may be needed.", commitErr)
			}
			return commitErr
		}

		op = switchOp{From: fromRef, To: toRef, Warnings: warningsOut}
		displayAfterLock = true
		return nil
	})
	if err != nil {
		return switchOp{}, err
	}

	if displayAfterLock {
		if emitOutput {
			printOut(printer.Accent("Switched to") + " Account-" + targetAccount + " (" + targetEmail + ")")
			if PostSwitchList != nil {
				if lerr := PostSwitchList(s); lerr != nil {
					if s.Log != nil {
						s.Log.Warningf("Post-switch usage display failed: %v", lerr)
					}
					printOut(printer.Dimmed("  (usage display unavailable — run `tycswap --list` to retry)"))
				}
			}
			printOut("")
			printSwitchFollowup(s, targetAccount, plan)
			printOut("")
		}
		replanNewActive(s, targetAccount, targetEmail, targetOrg)
	}
	return op, nil
}

// directActivate is the direct-activation path (fresh machine / unmanaged live /
// --force): write the target's stored backup over the live login without backing
// up the current one. It returns the op; the display and replan happen under the
// lock here (Python returns inside the with-block on this path).
func directActivate(s *store.Store, data *store.SequenceData, targetAccount, targetEmail, targetOrg string, fromRef, toRef map[string]any, curOK bool, curEmail, currentAccount string, forceActivate, emitOutput bool, warningsOut []string) (switchOp, error) {
	targetCreds, _ := s.ReadAccountCredentials(targetAccount, targetEmail)
	targetConfig, _ := s.ReadAccountConfig(targetAccount, targetEmail)
	if targetCreds == "" {
		return switchOp{}, cerr.Switch("Account-%s has no stored credentials. Re-add with: tycswap --add-account --slot %s", targetAccount, targetAccount)
	}
	if targetConfig == "" {
		return switchOp{}, cerr.Switch("Account-%s has no stored config backup. Re-add with: tycswap --add-account --slot %s", targetAccount, targetAccount)
	}
	var targetConfigData map[string]any
	if err := json.Unmarshal([]byte(targetConfig), &targetConfigData); err != nil {
		return switchOp{}, cerr.Switch("Invalid backup config: %v", err)
	}
	targetOAuth, oauthOK := oauthSection(targetConfigData)
	if !oauthOK {
		return switchOp{}, cerr.Switch("Invalid oauthAccount in backup")
	}
	// What the activation does to Claude Code's settings.json, decided (and
	// refused) before anything is written; its snapshot is the rollback's.
	plan, err := planProfile(s, data, targetAccount, targetCreds)
	if err != nil {
		return switchOp{}, err
	}

	// Snapshot live state for rollback (only when a live identity exists).
	var rollbackCreds, restoreCreds string
	haveRollbackCreds := false
	var rollbackConfigText string
	haveRollbackConfig := false
	if curOK {
		rc, ok := readActive(s)
		if !ok {
			return switchOp{}, cerr.CredentialRead("Cannot snapshot live credentials before activation")
		}
		rollbackCreds = rc
		// What a failed commit writes back. With no login or key live it is
		// the raw OAuth text, so a credential holding only seat-wide keys is
		// restored instead of replaced by an empty one (DESIGN A30).
		restoreCreds = rc
		if rc == "" {
			restoreCreds = s.Creds.ReadLiveOAuth()
		}
		haveRollbackCreds = true
		text, exists, err := readConfigText()
		if err != nil {
			return switchOp{}, cerr.Config("Cannot snapshot live config before activation: %v", err)
		}
		if exists {
			rollbackConfigText = text
			haveRollbackConfig = true
		}
	}

	// Invariant II stash: the replaced live credential would otherwise have no
	// surviving copy. A live blob that differs from the target only by its
	// seat-wide keys is not replaced: that part is carried over the write.
	if haveRollbackCreds && rollbackCreds != "" && !sameAccountBytes(rollbackCreds, targetCreds) && curOK {
		slotForStash := currentAccount
		if slotForStash == "" {
			slotForStash = "unmanaged"
		}
		if _, err := stashLiveCredential(s, rollbackCreds, "displaced-live-login", slotForStash, nil); err != nil {
			if !forceActivate {
				return switchOp{}, cerr.Switch("Could not preserve the live credential before activation (safety-copy write failed: %v); aborting rather than destroying it", err)
			}
			msg := "Could not preserve the replaced live credential (safety-copy write failed: " +
				errString(err) + ") — proceeding because --force explicitly rewrites the live login."
			if emitOutput {
				printWarning(msg)
			} else {
				warningsOut = append(warningsOut, msg)
			}
		}
	}

	credsWritten := false
	profileTouched := false
	configWritten := false
	commit := func() error {
		// Read the live config before anything is written: a corrupt or
		// unreadable one aborts the activation instead of being replaced.
		if _, err := readConfigForUpdate(); err != nil {
			return err
		}
		// Clearing does not undo itself when it fails half-way: mark it
		// first, so the rollback writes the original credential back.
		credsWritten = plan.apply != nil
		if err := writeActiveFor(s, plan, targetCreds); err != nil {
			return err
		}
		credsWritten = true
		// Marked before the write: a half-done one (the record written,
		// settings.json not) is put back by the snapshot just the same.
		profileTouched = plan.touches()
		if err := plan.commit(s); err != nil {
			return err
		}
		// Read it again, as the normal switch does: the credential write
		// changes it too (a managed key and its approval are stored there, an
		// OAuth write drops the key), so the copy read above would lose the new
		// key or put the replaced one back.
		existing, err := readConfigForUpdate()
		if err != nil {
			return err
		}
		// Only the oauthAccount is ever taken from the stored config. With no
		// live config the result is {"oauthAccount": …} alone: a stored config
		// is never written whole, so keys an import or an old full backup
		// carried (mcpServers, allowed tools, hooks) cannot reach ~/.claude.json.
		if existing == nil {
			existing = map[string]any{}
		}
		existing["oauthAccount"] = targetOAuth
		if err := writeConfigJSON(existing); err != nil {
			return err
		}
		configWritten = true
		targetInt, _ := parseInt(targetAccount)
		data.ActiveAccountNumber = &targetInt
		data.LastUpdated = storeTimestamp(s)
		return s.WriteSequence(data)
	}
	if err := commit(); err != nil {
		if configWritten && haveRollbackConfig {
			if rerr := writeConfigText(rollbackConfigText); rerr != nil && s.Log != nil {
				s.Log.Errorf("Failed to rollback config: %v", rerr)
			}
		}
		if profileTouched {
			if rerr := plan.restore(); rerr != nil && s.Log != nil {
				s.Log.Errorf("Failed to rollback Claude Code's settings: %v", rerr)
			}
		}
		if credsWritten && haveRollbackCreds {
			// With nothing at all live before (no login, no key, no
			// seat-wide part) the store is cleared again rather than
			// handed an empty credential.
			restore := func() error { return s.Creds.WriteActive(restoreCreds) }
			if restoreCreds == "" {
				restore = s.Creds.ClearActive
			}
			if rerr := restore(); rerr != nil && s.Log != nil {
				s.Log.Errorf("Failed to rollback credentials: %v", rerr)
			}
		}
		return switchOp{}, err
	}

	plan.warn(emitOutput, &warningsOut)
	if s.Log != nil {
		if forceActivate && curOK {
			s.Log.Infof("Activated account %s (forced, backup of current login skipped)", targetAccount)
		} else {
			s.Log.Infof("Activated account %s (no prior live account)", targetAccount)
		}
	}
	if emitOutput {
		printOut(printer.Accent("Activated") + " Account-" + targetAccount + " (" + targetEmail + ")")
		printOut("")
		printSwitchFollowup(s, targetAccount, plan)
		printOut("")
	}
	replanNewActive(s, targetAccount, targetEmail, targetOrg)
	return switchOp{From: fromRef, To: toRef, Warnings: warningsOut}, nil
}

// normalSwitchBody runs steps 1–5 of the normal switch under the lock, recording
// each completed step on tx for reverse-order rollback. It returns the first
// error (the caller decides rolled-back vs rollback-also-failed).
func normalSwitchBody(s *store.Store, data *store.SequenceData, tx *switchTransaction, targetAccount, targetEmail, currentAccount, currentEmail, originalCreds, originalConfig string, curEndpoint bool, prov *Provenance, emitOutput bool, warningsOut *[]string) error {
	// What the switch does to Claude Code's settings.json (DESIGN A46),
	// decided and refused before anything is written, the outgoing backup
	// included. An endpoint target is never the outgoing slot (that one has
	// nothing live, so a switch to it is "Already on"), so step 1 cannot
	// change the key planned from.
	plannedCreds, _ := s.ReadAccountCredentials(targetAccount, targetEmail)
	plan, err := planProfile(s, data, targetAccount, plannedCreds)
	if err != nil {
		return err
	}
	tx.plan = plan

	// Step 1: back up the outgoing slot, classified by the ownership oracle.
	// An account with a base URL is not classified: its credential is the
	// stored key, which the switch onto it wrote into settings.json, and
	// nothing in Claude Code's credential store is its own (DESIGN A46).
	var kind, foreignSlot string
	switch {
	case !curEndpoint:
		kind, foreignSlot = classifyOutgoing(s, currentAccount, currentEmail, originalCreds, prov, data)
	case originalCreds != "" && !ownStoredKey(s, currentAccount, currentEmail, originalCreds):
		// A login written into the store meanwhile, by something that left
		// the endpoint account's identity in place: not this account's, so
		// it is preserved, never stored over the key.
		kind = "alien"
	default:
		// Nothing live, or the account's own key still in the managed
		// store (a URL added to the active account since it was switched
		// to): its stored key is its credential either way.
		kind = "own-bytes"
	}
	switch kind {
	case "foreign", "alien":
		if _, err := stashLiveCredential(s, originalCreds, kind, currentAccount, prov.Resolved); err != nil {
			return err // abort before overwriting the live store
		}
		var msg string
		if kind == "foreign" {
			msg = "Credential ownership mismatch detected. The live credential was preserved and " +
				"was not written into Account-" + currentAccount + ". If Account-" + foreignSlot +
				" later cannot authenticate, log in as it and run: tycswap add --slot " + foreignSlot
		} else {
			msg = "The live login does not match a managed account. It was preserved and not " +
				"written into Account-" + currentAccount + ". If you need that account, log in as " +
				"it and run: tycswap add"
		}
		if emitOutput {
			printWarning(msg)
		} else {
			*warningsOut = append(*warningsOut, msg)
		}
	case "foreign-synced":
		msg := "Credential ownership mismatch detected. The live credential already matches Account-" +
			foreignSlot + "'s stored backup, so nothing was written into Account-" + currentAccount + "."
		if emitOutput {
			printWarning(msg)
		} else {
			*warningsOut = append(*warningsOut, msg)
		}
	case "unresolved":
		if err := s.WriteAccountCredentials(currentAccount, currentEmail, oauth.AccountOnly(originalCreds)); err != nil {
			return err
		}
		if err := s.WriteAccountConfig(currentAccount, currentEmail, originalConfig); err != nil {
			return err
		}
		if s.Log != nil {
			s.Log.Infof("Backed up account %s (lineage differs from the stored backup and ownership could not be verified — pre-fix backup)", currentAccount)
		}
	case "own-bytes":
		if err := s.WriteAccountConfig(currentAccount, currentEmail, originalConfig); err != nil {
			return err
		}
		if s.Log != nil {
			s.Log.Infof("Backed up account %s (config only; credentials unchanged)", currentAccount)
		}
	default: // own-family / own-rotated
		// The backup is the account part only: the live seat-wide keys are the
		// seat's and stay live across the switch.
		if err := s.WriteAccountCredentials(currentAccount, currentEmail, oauth.AccountOnly(originalCreds)); err != nil {
			return err
		}
		if err := s.WriteAccountConfig(currentAccount, currentEmail, originalConfig); err != nil {
			return err
		}
		if kind == "own-rotated" && prov.Resolved != nil {
			if err := backfillUUIDInData(data, currentAccount, prov.Resolved.UUID); err != nil {
				return err
			}
		}
		if s.Log != nil {
			s.Log.Infof("Backed up account %s", currentAccount)
		}
	}

	// Step 2: retrieve target.
	targetCreds, _ := s.ReadAccountCredentials(targetAccount, targetEmail)
	targetConfig, _ := s.ReadAccountConfig(targetAccount, targetEmail)
	if targetCreds == "" {
		return cerr.Switch("Account-%s has no stored credentials. Re-add with: tycswap --add-account --slot %s", targetAccount, targetAccount)
	}
	if targetConfig == "" {
		return cerr.Switch("Account-%s has no stored config backup. Re-add with: tycswap --add-account --slot %s", targetAccount, targetAccount)
	}

	// Step 3: activate target credentials. The live seat-wide keys (the seat's
	// MCP server logins and client secrets) ride over the stored account blob;
	// rollback below restores the original bytes verbatim through WriteActive.
	// An account with a base URL stores nothing here: every login leaves the
	// store, and its key goes into settings.json in step 3b. Clearing does
	// not undo itself when it fails half-way, so its step is recorded first
	// and the rollback writes the original credential back.
	clearing := plan.apply != nil
	if clearing {
		tx.recordStep("credentials_written")
	}
	if err := writeActiveFor(s, plan, targetCreds); err != nil {
		return err
	}
	if !clearing {
		tx.recordStep("credentials_written")
	}
	if s.Log != nil {
		s.Log.Infof("Wrote target credentials")
	}

	// Step 3b: write the endpoint into settings.json, or put back what it
	// held before one was written. Recorded before the write, so a half-done
	// write is rolled back from the snapshot too.
	if plan.touches() {
		tx.recordStep("profile_written")
		if err := plan.commit(s); err != nil {
			return err
		}
		plan.warn(emitOutput, warningsOut)
	}

	// Step 4: splice the target oauthAccount into the live config.
	var targetConfigData map[string]any
	if err := json.Unmarshal([]byte(targetConfig), &targetConfigData); err != nil {
		return err
	}
	oauthSec, ok := oauthSection(targetConfigData)
	if !ok {
		return cerr.Switch("Invalid oauthAccount in backup")
	}
	cfg, err := readConfigForUpdate()
	if err != nil {
		return err
	}
	if cfg == nil {
		// Python would TypeError here (None["oauthAccount"]); an exception →
		// rollback of credentials_written. Surface the same failure.
		return cerr.Config("Claude config file not found")
	}
	cfg["oauthAccount"] = oauthSec
	if err := writeConfigJSON(cfg); err != nil {
		return err
	}
	tx.recordStep("config_written")
	if s.Log != nil {
		s.Log.Infof("Updated config file")
	}

	// Step 5: update sequence state.
	targetInt, _ := parseInt(targetAccount)
	data.ActiveAccountNumber = &targetInt
	data.LastUpdated = storeTimestamp(s)
	if err := s.WriteSequence(data); err != nil {
		return err
	}
	tx.recordStep("sequence_updated")
	if s.Log != nil {
		// The A4 switch-history INFO line — exact wording is a hard interop
		// contract (the TUI history reader parses it).
		s.Log.Infof("Switched from account %s to %s", currentAccount, targetAccount)
	}
	return nil
}

// backfillUUIDInData sets a missing slot uuid from the resolved identity on the
// in-hand sequence data (own-rotated), so the pending step-5 write persists it.
func backfillUUIDInData(data *store.SequenceData, num, uuid string) error {
	if uuid == "" {
		return nil
	}
	rec, ok := accountRec(data, num)
	if !ok {
		return nil
	}
	if recStr(rec, "uuid") != "" {
		return nil
	}
	rec["uuid"] = uuid
	nb, err := encodeRec(rec)
	if err != nil {
		return err
	}
	data.Accounts[num] = nb
	return nil
}

// oauthSection returns the truthy oauthAccount object from a parsed backup config
// (Python `if not oauth_section`: None or empty dict is falsy → invalid).
func oauthSection(cfg map[string]any) (map[string]any, bool) {
	v, ok := cfg["oauthAccount"]
	if !ok || v == nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, false
	}
	return m, true
}

// printSwitchFollowup prints the post-switch note keyed to where the active
// credential write landed (spec 02§8.4). A restart is never required, except
// after a switch onto an API-key account: that changes how Claude Code
// authenticates, and a running session keeps its old login (DESIGN A33). A
// switch that rewrote Claude Code's settings.json says what a running session
// does with that instead (DESIGN A46): it re-reads the file and adds what it
// finds, so it can take up an endpoint at once but keeps one that was taken
// out until it is restarted.
func printSwitchFollowup(s *store.Store, target string, plan *profilePlan) {
	switch {
	case plan != nil && plan.applied:
		printOut(printer.Dimmed(EndpointAppliedNote(ccsettings.Host(plan.apply.BaseURL))))
		return
	case plan != nil && plan.reverted:
		printOut(printer.Dimmed(EndpointRevertedNote))
		return
	}
	if s.AccountKindFor(target) == "api_key" {
		printOut(printer.Dimmed(APIKeyRestartNote))
		return
	}
	backend := s.Creds.LastActiveBackend()
	if backend == "" {
		// No write recorded (defensive; a switch always writes): fall back to
		// the platform routing hint (macOS ⇒ keychain).
		if s.Platform == platform.MacOS {
			backend = "keychain"
		} else {
			backend = "file"
		}
	}
	if backend == "keychain" {
		printOut(printer.Dimmed("Restart Claude Code to apply immediately — otherwise the session can take up to ~30 seconds to pick up the new account."))
	} else {
		printOut(printer.Dimmed("New account is active on your next message — no restart needed."))
	}
}

// configReadError maps a non-nil ~/.claude.json read error to the domain error.
// Only a genuine absence is "not found" (fs.ErrNotExist); permission is called
// out; and every other cause — a directory at the path, an I/O error — surfaces
// with its real message rather than being misreported as "not found". This
// mirrors Python, where FileNotFoundError/PermissionError are caught explicitly
// and any other OSError propagates raw with its own message.
func configReadError(err error) error {
	switch {
	case os.IsPermission(err):
		return cerr.Config("Permission denied reading Claude config")
	case errors.Is(err, fs.ErrNotExist):
		return cerr.Config("Claude config file not found")
	default:
		return cerr.Config("Failed to read Claude config: %v", err).Wrap(err)
	}
}

// errString renders an error for message interpolation.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
