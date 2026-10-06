package switching

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/statusline"
)

// After a switch onto an API-key account the file names it under the key a
// reader computes from the live ~/.claude.json (organization null -> ""), and
// the key itself never reaches the file.
func TestSwitchPublishesTheLiveAccount(t *testing.T) {
	s := newTestStore(t, nil)
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
		"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
	}))
	ca := oauthCreds("acc-a", "ref-a")
	seedBackup(t, s, "1", oauthSeatEmail, ca, "")
	if err := s.WriteAccountCredentials("2", apiKeySeatEmail, apiKeySeatKey); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAccountConfig("2", apiKeySeatEmail, `{"oauthAccount": {"emailAddress": "`+apiKeySeatEmail+`", "accountUuid": "", "organizationUuid": null, "organizationName": null}}`); err != nil {
		t.Fatal(err)
	}
	seedLive(t, s, oauthSeatEmail, "", ca)

	ApproveAPIKeySwitch("2")
	if _, err := SwitchTo(s, "2", true, false); err != nil {
		t.Fatalf("SwitchTo(2): %v", err)
	}
	raw, err := os.ReadFile(statusline.Path(s.BackupDir()))
	if err != nil {
		t.Fatal(err)
	}
	var doc statusline.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	live, _ := liveConfig(t, s)["oauthAccount"].(map[string]any)
	email, _ := live["emailAddress"].(string)
	org, _ := live["organizationUuid"].(string)
	if acct, ok := doc.Accounts[email+"|"+org]; !ok || acct.Slot != 2 {
		t.Errorf("live key %q -> %+v (present %v); accounts %v", email+"|"+org, acct, ok, doc.Accounts)
	}
	if strings.Contains(string(raw), apiKeySeatKey) {
		t.Errorf("statusline.json carries the API key:\n%s", raw)
	}
}

// A statusline.json that cannot be written (a directory in its place) is
// logged and the switch stands.
func TestSwitchSucceedsWhenTheStatuslineCannotBeWritten(t *testing.T) {
	s, _, cb := twoAccountStore(t)
	if err := os.Remove(statusline.Path(s.BackupDir())); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statusline.Path(s.BackupDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchTo(s, "2", true, false); err != nil {
		t.Fatalf("SwitchTo with an unwritable statusline.json: %v", err)
	}
	if got := readActiveCreds(t, s); got != cb {
		t.Errorf("live credential = %q, want account 2's", got)
	}
	if data, _ := s.ReadSequence(); data.ActiveAccountNumber == nil || *data.ActiveAccountNumber != 2 {
		t.Errorf("active = %v, want 2", data.ActiveAccountNumber)
	}
}
