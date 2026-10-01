package switching

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// TestConfigReadErrorNotFoundOnlyForENOENT: a non-ENOENT read error on
// ~/.claude.json (here: a directory at the path) must NOT be misreported as
// "Claude config file not found" — it surfaces with the real cause. Only a
// genuine absence maps to "not found" (FINDING 10).
func TestConfigReadErrorNotFoundOnlyForENOENT(t *testing.T) {
	// A real EISDIR-class error: reading a directory as a file, matching how
	// readConfigText would fail if ~/.claude.json were a directory.
	dir := t.TempDir()
	_, dirErr := os.ReadFile(dir)
	if dirErr == nil {
		t.Fatal("reading a directory should have errored")
	}
	err := configReadError(dirErr)
	if err == nil {
		t.Fatal("configReadError(dir read error) = nil")
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("directory read error misreported as not-found: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Failed to read Claude config") {
		t.Fatalf("error should carry the real cause, got %q", err.Error())
	}
	// The underlying cause is preserved on the chain.
	if !errorsIsCause(err, dirErr) {
		t.Fatalf("underlying cause not wrapped: %v", err)
	}

	// A genuine absence still maps to the "not found" message.
	missing := os.ErrNotExist
	nf := configReadError(missing)
	if !strings.Contains(nf.Error(), "Claude config file not found") {
		t.Fatalf("ENOENT should map to not-found, got %q", nf.Error())
	}
	if cerr.TypeName(nf) != "ConfigError" {
		t.Fatalf("want ConfigError kind, got %q", cerr.TypeName(nf))
	}
}

// errorsIsCause reports whether target is somewhere in err's Unwrap chain.
func errorsIsCause(err, target error) bool {
	for e := err; e != nil; {
		if e == target {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// TestSwitchEmptyCurrentCredsGuard: an empty active-credential read (a Keychain
// timeout returns "") must NOT overwrite the departing slot's backup — the
// switch fails with a CredentialReadError (spec 02§8).
func TestSwitchEmptyCurrentCredsGuard(t *testing.T) {
	s := newTestStore(t, nil)
	ca := oauthCreds("acc-a", "ref-a")
	cb := oauthCreds("acc-b", "ref-b")
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": "a@x.com", "organizationUuid": ""}),
		"2": record(map[string]any{"email": "b@x.com", "organizationUuid": ""}),
	}))
	seedBackup(t, s, "1", "a@x.com", ca, "")
	seedBackup(t, s, "2", "b@x.com", cb, "")
	seedLive(t, s, "a@x.com", "", "") // empty live credential

	_, err := SwitchTo(s, "2", true, false)
	if err == nil || !strings.Contains(err.Error(), "Current account credential is empty") {
		t.Fatalf("err = %v, want CredentialReadError(empty)", err)
	}
	// The outgoing slot's backup is intact.
	if got, _ := s.ReadAccountCredentials("1", "a@x.com"); got != ca {
		t.Fatalf("outgoing backup was clobbered by an empty-read switch")
	}
}

// TestSwitchForeignCredentialPreserved: when the live credential resolves
// (via the profile endpoint) to a DIFFERENT managed slot, the switch must not
// write it into the outgoing slot — it is stashed, a warning rides back, and the
// outgoing backup stays intact (issue #117).
func TestSwitchForeignCredentialPreserved(t *testing.T) {
	fake := &oauth.FakeClient{
		ProfileFn: func(_ context.Context, accessToken string) *oauth.Identity {
			if accessToken == "live-access" {
				return &oauth.Identity{UUID: "uuid-2", Email: "b@x.com", OrgUUID: ""}
			}
			return nil
		},
	}
	s := newTestStore(t, fake)

	backup1 := oauthCreds("a1", "ref1")
	backup2 := oauthCreds("b2", "ref2")
	liveCreds := oauthCreds("live-access", "ref-live") // belongs to account 2 by identity
	recs := map[string]json.RawMessage{
		"1": record(map[string]any{"email": "a@x.com", "organizationUuid": "", "uuid": "uuid-1"}),
		"2": record(map[string]any{"email": "b@x.com", "organizationUuid": "", "uuid": "uuid-2"}),
	}
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, recs))
	seedBackup(t, s, "1", "a@x.com", backup1, "")
	seedBackup(t, s, "2", "b@x.com", backup2, "")
	// Live login says account 1 (email a@x.com) but the live bytes are foreign.
	seedLive(t, s, "a@x.com", "", liveCreds)

	out, err := Switch(s, nil, true, nil, nil)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	m := asMap(t, out)
	warnings, _ := m["warnings"].([]string)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "Credential ownership mismatch detected") && strings.Contains(w, "not written") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a foreign-credential warning, got %v", warnings)
	}
	// The outgoing slot 1 backup is untouched (the foreign live bytes were NOT
	// written into it).
	if got, _ := s.ReadAccountCredentials("1", "a@x.com"); got != backup1 {
		t.Fatalf("outgoing backup was poisoned with the foreign credential")
	}
	// A safety stash was written.
	stashed, _ := s.Creds.ListUnclaimed()
	if len(stashed) == 0 {
		t.Fatalf("expected the foreign credential to be stashed")
	}
	// The switch still completed onto account 2.
	if got := readActiveCreds(t, s); got != backup2 {
		t.Fatalf("switch did not activate account 2")
	}
}

// mcpSwitchFixture seeds two managed accounts, slot 1 live, with the seat's MCP
// server login in the live credentials file (not in any backup).
func mcpSwitchFixture(t *testing.T) (s *store.Store, backup1, backup2, live string) {
	t.Helper()
	s = newTestStore(t, nil)
	backup1 = oauthCreds("a1", "ref1")
	backup2 = oauthCreds("b2", "ref2")
	live = withMCPOAuth(t, backup1, "srv|1111", "mcp-live")
	recs := map[string]json.RawMessage{
		"1": record(map[string]any{"email": "a@x.com", "organizationUuid": "", "uuid": "uuid-1"}),
		"2": record(map[string]any{"email": "b@x.com", "organizationUuid": "", "uuid": "uuid-2"}),
	}
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, recs))
	seedBackup(t, s, "1", "a@x.com", backup1, "")
	seedBackup(t, s, "2", "b@x.com", backup2, "")
	seedLive(t, s, "a@x.com", "", live)
	return s, backup1, backup2, live
}

// TestSwitchKeepsLiveMCPOAuth: a normal switch writes the target's account
// blob with the live mcpOAuth carried over it, so the seat's MCP server logins
// survive the change of Claude account.
func TestSwitchKeepsLiveMCPOAuth(t *testing.T) {
	s, backup1, _, _ := mcpSwitchFixture(t)
	if _, err := SwitchTo(s, "2", true, false); err != nil {
		t.Fatalf("SwitchTo: %v", err)
	}
	got := readActiveCreds(t, s)
	if oauth.ExtractAccessToken(got) != "b2" {
		t.Fatalf("live account after the switch = %q, want account 2", got)
	}
	if mcpTokenOf(t, got, "srv|1111") != "mcp-live" {
		t.Fatalf("the live mcpOAuth did not survive the switch: %s", got)
	}
	// The outgoing slot's backup holds the account only — the live file only
	// gained mcpOAuth, so the classifier saw own-bytes and left the stored
	// bytes alone.
	stored, _ := s.ReadAccountCredentials("1", "a@x.com")
	if stored != backup1 {
		t.Fatalf("outgoing backup = %q, want the untouched %q", stored, backup1)
	}
	if hasMCPOAuth(t, stored) {
		t.Fatalf("outgoing backup carries the seat's mcpOAuth: %s", stored)
	}
}

// TestSwitchBacksUpTheAccountOnly: when the outgoing credential does need a
// backup (its access token rotated), the backup still carries no mcpOAuth.
func TestSwitchBacksUpTheAccountOnly(t *testing.T) {
	s, _, _, _ := mcpSwitchFixture(t)
	rotated := withMCPOAuth(t, oauthCreds("a1-rotated", "ref1"), "srv|1111", "mcp-live")
	seedLive(t, s, "a@x.com", "", rotated)
	if _, err := SwitchTo(s, "2", true, false); err != nil {
		t.Fatalf("SwitchTo: %v", err)
	}
	stored, _ := s.ReadAccountCredentials("1", "a@x.com")
	if oauth.ExtractAccessToken(stored) != "a1-rotated" {
		t.Fatalf("outgoing backup = %q, want the rotated account", stored)
	}
	if hasMCPOAuth(t, stored) {
		t.Fatalf("outgoing backup carries the seat's mcpOAuth: %s", stored)
	}
	if mcpTokenOf(t, readActiveCreds(t, s), "srv|1111") != "mcp-live" {
		t.Fatal("the live mcpOAuth did not survive the switch")
	}
}

// TestDirectActivateKeepsLiveMCPOAuth: the direct-activation path (--force,
// also what `add --login --switch` takes) carries the live mcpOAuth too.
func TestDirectActivateKeepsLiveMCPOAuth(t *testing.T) {
	s, _, _, _ := mcpSwitchFixture(t)
	if _, err := SwitchTo(s, "2", true, true); err != nil {
		t.Fatalf("SwitchTo --force: %v", err)
	}
	got := readActiveCreds(t, s)
	if oauth.ExtractAccessToken(got) != "b2" {
		t.Fatalf("live account after the activation = %q, want account 2", got)
	}
	if mcpTokenOf(t, got, "srv|1111") != "mcp-live" {
		t.Fatalf("the live mcpOAuth did not survive the activation: %s", got)
	}
	// The displaced live login is stashed account-only.
	for _, entry := range unclaimedEntries(t, s) {
		if hasMCPOAuth(t, entry) {
			t.Fatalf("the unclaimed stash carries the seat's mcpOAuth: %s", entry)
		}
		if oauth.ExtractAccessToken(entry) != "a1" {
			t.Fatalf("stash = %q, want the displaced account", entry)
		}
	}
	if len(unclaimedEntries(t, s)) != 1 {
		t.Fatalf("want one stash entry for the displaced login")
	}
}

// TestDirectActivateSameAccountWithMCPOAuthStashesNothing: forcing the active
// account back on when the live file differs from its backup only by mcpOAuth
// displaces nothing, so nothing is stashed.
func TestDirectActivateSameAccountWithMCPOAuthStashesNothing(t *testing.T) {
	s, _, _, _ := mcpSwitchFixture(t)
	if _, err := SwitchTo(s, "1", true, true); err != nil {
		t.Fatalf("SwitchTo --force: %v", err)
	}
	if n := len(unclaimedEntries(t, s)); n != 0 {
		t.Fatalf("%d stash entries, want none: only mcpOAuth differed", n)
	}
	if mcpTokenOf(t, readActiveCreds(t, s), "srv|1111") != "mcp-live" {
		t.Fatal("the live mcpOAuth did not survive the re-activation")
	}
}

// unclaimedEntries decodes every stash entry file under the credentials dir.
func unclaimedEntries(t *testing.T, s *store.Store) []string {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(s.CredentialsDir, ".unclaimed-*.enc"))
	var out []string
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatalf("stash entry %s: %v", p, err)
		}
		out = append(out, string(dec))
	}
	return out
}

// TestSwitchRollbackRestoresLiveBytesVerbatim: a failure after the credential
// write rolls the live file back to the exact pre-switch bytes — WriteActive,
// not the splice.
func TestSwitchRollbackRestoresLiveBytesVerbatim(t *testing.T) {
	s, _, _, live := mcpSwitchFixture(t)
	// Step 4 fails after credentials_written: the target's stored config has no
	// oauthAccount to splice into the live ~/.claude.json.
	if err := s.WriteAccountConfig("2", "b@x.com", `{"other": 1}`); err != nil {
		t.Fatal(err)
	}
	_, err := SwitchTo(s, "2", true, false)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v, want a rolled-back switch failure", err)
	}
	if got := readActiveCreds(t, s); got != live {
		t.Fatalf("rollback wrote\n%s\nwant the original live bytes verbatim\n%s", got, live)
	}
}
