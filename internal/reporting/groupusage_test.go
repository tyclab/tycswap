package reporting

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/testutil"
)

func TestGroupExpiryRemainsVisibleInReadOnlySnapshots(t *testing.T) {
	clk := testutil.FixedClock(t, fixedNow)
	s := newStore(t, clk, nil)
	writeSequenceRaw(t, s, `{"activeAccountNumber":null,"sequence":[2],"accounts":{"2":{"email":"group@example.test","organizationUuid":""}}}`)
	account := groups.Account{Number: "2", Email: "group@example.test"}
	dir := groups.ProfileDir(s.BackupDir(), groups.Opus)
	for path, value := range map[string]any{
		groups.ActivePath(s.BackupDir(), groups.Opus): account,
		filepath.Join(dir, ".claude.json"):            map[string]any{"oauthAccount": map[string]any{"emailAddress": account.Email, "organizationUuid": ""}},
	} {
		if err := groups.WriteJSON(path, value); err != nil {
			t.Fatal(err)
		}
	}
	registry := groups.Registry{Version: 1, Claims: map[string]groups.Owner{"2": {Scope: "opus", ProfileDir: dir, Account: account}}}
	if err := registry.Save(s.BackupDir()); err != nil {
		t.Fatal(err)
	}
	for _, expired := range []bool{true, false} {
		expiry := clk.Now().UnixMilli() + 3600_000
		if expired {
			expiry = clk.Now().UnixMilli() - 3600_000
		}
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(oauthCreds("group-access", "group-refresh", expiry)), 0o600); err != nil {
			t.Fatal(err)
		}
		infos := BuildAccountsInfo(s)
		if len(infos) != 1 || infos[0].IsActive || infos[0].Creds == "" {
			owner, err := s.CredentialOwner("2")
			t.Fatalf("group fixture was not readable: accounts=%d owner=%+v error=%v", len(infos), owner, err)
		}
		got := CollectUsageEntries(s, infos, map[string]bool{})["2"].Sentinel
		want := ""
		if expired {
			want = jsonout.UsageTokenExpired
		}
		if got != want {
			t.Fatalf("expired=%v: sentinel %q, want %q", expired, got, want)
		}
	}
}
