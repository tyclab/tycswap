package cli

import "testing"

func TestSessionLoginWithDuplicatedBackupHasUnknownOwner(t *testing.T) {
	sw, _, _ := mythosTickFixture(t, 20, 30, true)
	bindTickSession(t, sw, tickSessionA, "opus", sw.DefaultProfileDir())
	if err := sw.WriteAccountCredentials("2", fableEmail("2"), defaultLive(t, sw)); err != nil {
		t.Fatal(err)
	}
	api, ok := any(&dashboard{sw: sw}).(interface{ SessionLogin(string) (string, string) })
	if !ok {
		t.Fatal("linked session has no field for unknown account ownership")
	}
	login, number := api.SessionLogin(tickSessionA)
	if login != "default login" || number != "unknown" {
		t.Fatalf("shared configuration names Account-1, but two backups match the live credential: got %q %q", login, number)
	}
}
