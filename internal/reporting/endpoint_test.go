// status, list and the snapshot for an API-key account with a base URL
// (DESIGN A46). Its key is not shaped like an Anthropic key and, while it is
// active, nothing is in Claude Code's credential store: it still reads as an
// API key, never as "no credentials", and every surface carries the URL —
// whole in JSON, the host in human output.
package reporting

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	gwEmail = "gw@x.com"
	gwURL   = "https://gw.example.com/anthropic"
)

// TestEndpointAccountInStatusListAndSnapshot: slot 1 is a subscription
// account, slot 2 an endpoint account with a gateway-shaped key. With slot 2
// live, its identity is in ~/.claude.json and nothing is in the credential
// store.
func TestEndpointAccountInStatusListAndSnapshot(t *testing.T) {
	for _, active := range []string{"1", "2"} {
		t.Run("active "+active, func(t *testing.T) {
			s := newStore(t, nil, nil)
			writeSequenceRaw(t, s, `{"activeAccountNumber": `+active+`, "lastUpdated": "2026-07-17T12:00:00Z", "sequence": [1, 2], "accounts": {`+
				`"1": {"email": "a@x.com", "organizationUuid": "", "organizationName": ""},`+
				`"2": {"email": "`+gwEmail+`", "organizationUuid": "", "organizationName": "", "kind": "api_key", "baseUrl": "`+gwURL+`"}}}`)
			login := oauthCreds("acc-a", "ref-a", 4102444800000)
			writeBackup(t, s, "1", "a@x.com", login, `{"oauthAccount": {"emailAddress": "a@x.com", "organizationUuid": null}}`)
			writeBackup(t, s, "2", gwEmail, "sk-gw-0123456789", `{"oauthAccount": {"emailAddress": "`+gwEmail+`", "organizationUuid": null}}`)
			if active == "1" {
				writeLiveConfig(t, s, "a@x.com", "")
				writeActiveCreds(t, s, login)
			} else {
				writeLiveConfig(t, s, gwEmail, "")
			}

			got, err := ListAccounts(s, false, true, map[string]bool{})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(got)
			var list struct {
				Accounts []map[string]any `json:"accounts"`
			}
			if err := json.Unmarshal(b, &list); err != nil {
				t.Fatal(err)
			}
			if len(list.Accounts) != 2 {
				t.Fatalf("list rows = %v", list.Accounts)
			}
			if row := list.Accounts[1]; row["usageStatus"] != "api_key" || row["baseUrl"] != gwURL || row["active"] != (active == "2") {
				t.Errorf("endpoint row = %v, want api_key with the full baseUrl", row)
			}
			if _, present := list.Accounts[0]["baseUrl"]; present {
				t.Errorf("subscription row carries a baseUrl: %v", list.Accounts[0])
			}

			var human bytes.Buffer
			renderAccounts(&human, s, BuildAccountsInfo(s), CollectUsageEntries(s, BuildAccountsInfo(s), map[string]bool{}), false)
			if !strings.Contains(human.String(), "2: "+gwEmail+" [personal] → gw.example.com") || strings.Contains(human.String(), gwURL) {
				t.Errorf("human list shows not just the host:\n%s", human.String())
			}

			snap := Snapshot(s, map[string]bool{})
			if a := snap.Accounts[1]; a.BaseURL != gwURL || a.Kind != "api_key" || a.Usage.Sentinel != "api key" {
				t.Errorf("snapshot row = %+v", a)
			}

			if active != "2" {
				return
			}
			payload := buildStatusPayload(s)
			st, _ := payload["active"].(map[string]any)
			if st["usageStatus"] != "api_key" || st["baseUrl"] != gwURL || st["number"] != 2 {
				t.Errorf("status = %s, want the endpoint account as api_key with its baseUrl", mustJSON(t, payload))
			}
			var status bytes.Buffer
			renderStatus(&status, s)
			if !strings.Contains(status.String(), "Endpoint: gw.example.com") || strings.Contains(status.String(), gwURL) {
				t.Errorf("human status:\n%s", status.String())
			}
			if strings.Contains(status.String(), "no credentials") {
				t.Errorf("the active endpoint account reads as having no credentials:\n%s", status.String())
			}
		})
	}
}
