// status and list on an API-key seat whose credentials file holds nothing but
// seat-wide keys (DESIGN A29): the managed key behind that file is the live
// credential, so the active account shows as an API key, not as "no
// credentials".
package reporting

import (
	"encoding/json"
	"testing"
)

func TestStatusAndListShowAnAPIKeySeatWithASeatWideOnlyFile(t *testing.T) {
	const key = "sk-ant-api03-seat-fixture-0123456789"
	const email = "api-key-1@token.local"
	for _, file := range []string{
		`{"mcpOAuth":{"srv|1111":{"accessToken":"mcp-live"}}}`,
		`{"mcpOAuthClientConfig":{"srv|1111":{"clientSecret":"cs-fixture"}}}`,
	} {
		t.Run(file, func(t *testing.T) {
			s := newStore(t, nil, nil)
			writeSequenceRaw(t, s, `{"activeAccountNumber": 1, "lastUpdated": "2026-07-17T12:00:00Z", "sequence": [1], "accounts": {"1": {"email": "`+email+`", "organizationUuid": "", "organizationName": "", "kind": "api_key"}}}`)
			writeBackup(t, s, "1", email, key, `{"oauthAccount": {"emailAddress": "`+email+`", "organizationUuid": null}}`)
			writeLiveConfig(t, s, email, "")
			if err := s.Creds.WriteActive(key); err != nil {
				t.Fatal(err)
			}
			writeActiveCreds(t, s, file)

			payload := buildStatusPayload(s)
			active, _ := payload["active"].(map[string]any)
			if active["usageStatus"] != "api_key" {
				t.Errorf("status usageStatus = %v, want api_key (%s)", active["usageStatus"], mustJSON(t, payload))
			}

			got, err := ListAccounts(s, false, true, nil)
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
			if len(list.Accounts) != 1 || list.Accounts[0]["usageStatus"] != "api_key" {
				t.Errorf("list rows = %v, want the active account as api_key", list.Accounts)
			}
		})
	}
}
