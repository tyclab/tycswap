// provenance.go runs before any lock (network is forbidden under the FileLock) and is strictly advisory: failures are swallowed.

package switching

import (
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// Resolved is trustworthy only while the live bytes have not moved; the under-lock classifier re-checks byte equality.
type Provenance struct {
	Live     *string
	Resolved *oauth.Identity
}

// readActive reads Claude Code's active credential the way Python's
// _read_credentials does: a hard read failure (credstore's non-nil error,
// Python's None outcome) returns ok=false; otherwise the value ("" on a Keychain
// timeout or genuine absence).
func readActive(s *store.Store) (value string, ok bool) {
	v, _, err := s.Creds.ReadActive()
	if err != nil {
		return "", false
	}
	return v, true
}

// liveMatchesSlotBackup reports whether the live credential is provably the
// slot's stored lineage — byte or refresh-token-fingerprint equality against the
// backup (spec 02§7). Unreadable/empty live → true (keep the no-op: forcing a
// switch on missing evidence would fail later anyway); missing backup → false.
func liveMatchesSlotBackup(s *store.Store, slot, email string) bool {
	live, ok := readActive(s)
	if !ok {
		return true
	}
	if live == "" {
		return true
	}
	backup, _ := s.ReadAccountCredentials(slot, email)
	if backup == "" {
		return false
	}
	return sameAccountBytes(live, backup) || fingerprintEqual(live, backup)
}

func selfSwitchAction(s *store.Store, slot, email string) (string, *Provenance) {
	if liveMatchesSlotBackup(s, slot, email) {
		return "noop", nil
	}
	prov := prefetchLiveIdentity(s)
	if prov.Resolved == nil {
		if s.Log != nil {
			s.Log.Infof(
				"Live credential diverges from Account-%s's stored backup and "+
					"ownership could not be verified; self-switch left everything "+
					"untouched (pre-fix no-op).", slot)
		}
		return "noop-diverged", nil
	}
	return "reconcile", prov
}

// prefetchLiveIdentity resolves the live credential's owner BEFORE the locks are
// taken (spec 02§7 _prefetch_live_identity). Returns {live, resolved}; resolved
// is filled only when the live bytes diverge from the slot backup AND the
// profile endpoint answers. All failures are swallowed (advisory oracle). May
// hit the network — never call it with any lock held.
func prefetchLiveIdentity(s *store.Store) *Provenance {
	result := &Provenance{}
	live, ok := readActive(s)
	if !ok {
		return result
	}
	lv := live
	result.Live = &lv
	if live == "" {
		return result
	}
	email, orgUUID, identOK := s.GetCurrentAccount()
	if !identOK {
		return result
	}
	data, _ := s.ReadSequence()
	slot := s.FindAccountSlot(data, email, orgUUID)
	if slot == "" {
		return result
	}
	backup, _ := s.ReadAccountCredentials(slot, email)
	if sameAccountBytes(backup, live) || fingerprintEqual(backup, live) {
		return result // provenance already established locally
	}
	accessToken := oauth.ExtractAccessToken(live)
	if accessToken == "" {
		return result // raw API key / garbled JSON — nothing to resolve
	}
	if s.OAuth == nil {
		return result
	}
	if id := s.OAuth.Profile(bgCtx(), accessToken); id != nil {
		result.Resolved = id
	}
	return result
}

// sameAccountBytes reports whether two credentials are the same account bytes:
// identical, or identical once the seat-wide keys are set aside. A backup
// never carries them and the live file may, so a plain byte compare would
// read every MCP server login as a changed credential.
func sameAccountBytes(a, b string) bool {
	return oauth.SameAccountBlock(a, b)
}

// fingerprintEqual mirrors Python credential_fingerprint(a) == credential_fingerprint(b); callers guard empty inputs first.
func fingerprintEqual(a, b string) bool {
	fa := oauth.CredentialFingerprint(a)
	fb := oauth.CredentialFingerprint(b)
	if fa == nil || fb == nil {
		return fa == nil && fb == nil
	}
	return *fa == *fb
}
