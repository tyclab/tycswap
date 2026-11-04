package reporting

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestManagedHookReadsCapturedDefaultCredential(t *testing.T) {
	s := seedAtLimitStore(t, testutil.FixedClock(t, fixedNow))
	profile := groups.ProfileDir(s.BackupDir(), groups.Fable)
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, ".credentials.json"), []byte(oauthCreds("group-token", "group-refresh", 0)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", profile)
	infos := BuildAccountsInfo(s)
	if len(infos) != 2 || !infos[0].IsActive || oauth.ExtractAccessToken(infos[0].Creds) != "at1" {
		t.Fatal("group hook attributed its credential to the captured default account")
	}
}
