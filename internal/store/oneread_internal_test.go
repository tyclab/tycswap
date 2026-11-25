package store

import (
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
)

// One read saw claimedSession's profile idle and signed in as account 1; then
// a session starts in it. A group handoff's legacy step reads that afresh
// (DESIGN A116).
func TestLegacyHandoffReadsAfreshInOneRead(t *testing.T) {
	for name, guard := range map[string]func(s *Store, j *groups.Journal) error{
		"clear":   func(s *Store, j *groups.Journal) error { return s.ClearLegacyForGroup(j) },
		"restore": func(s *Store, j *groups.Journal) error { return s.restoreLegacyHandoff(j) },
	} {
		t.Run(name, func(t *testing.T) {
			s, dir := claimedSession(t)
			creds, _, err := s.ReadProfileCredentials(dir)
			if err != nil {
				t.Fatal(err)
			}
			owner := groups.Owner{Scope: "legacy:1", ProfileDir: dir, Account: groups.Account{Number: "1", Email: "one@example.test"}}
			journal := &groups.Journal{SourceOwner: &owner, SourceCredential: groups.Snapshot{Exists: true, Text: creds}}
			view := s.OneRead()
			if _, err := view.CredentialOwner("1"); err != nil {
				t.Fatal(err)
			}
			makeLiveSession(t, s, "1", "one@example.test")
			if err := guard(view, journal); err == nil || !strings.Contains(err.Error(), "the source legacy profile is running") {
				t.Fatalf("%s = %v, want the running profile", name, err)
			}
			if got, _, _ := s.ReadProfileCredentials(dir); got != creds {
				t.Fatalf("the running profile's credential changed: %q", got)
			}
		})
	}
}

// One read saw the default login as account 1, whose idle profile holds the
// live login's lineage; then the default config names an account tycswap does
// not manage. Discarding the profile's login reads that afresh.
func TestDiscardLegacyLoginReadsTheDefaultLoginAfresh(t *testing.T) {
	s, dir := claimedSession(t)
	creds, _, err := s.ReadProfileCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeGlobalConfig(t, s, `{"oauthAccount":{"emailAddress":"one@example.test","organizationUuid":""}}`)
	if err := s.Creds.WriteActive(creds); err != nil {
		t.Fatal(err)
	}
	view := s.OneRead()
	if email, _, ok := view.GetCurrentAccount(); !ok || email != "one@example.test" {
		t.Fatalf("fixture: live account %q, %v", email, ok)
	}
	writeGlobalConfig(t, s, `{"oauthAccount":{"emailAddress":"other@example.test","organizationUuid":""}}`)
	err = s.Lock.With(func() error { return view.DiscardLegacyLogin("1", creds) })
	if err == nil || !strings.Contains(err.Error(), "holds a login the live one does not continue") {
		t.Fatalf("DiscardLegacyLogin = %v, want the refusal for a live login of another account", err)
	}
	if got, _, _ := s.ReadProfileCredentials(dir); got != creds {
		t.Fatalf("the profile's login was dropped: %q", got)
	}
}
