// workspaces.go follows codex-auth's grouped-scope rules (docs/api.md) so listings do not add a request per scope forever:
// one request per chatgpt_user_id scope, only with >1 record, a workspace plan and an unnamed one. The server's names overwrite;
// a workspace not returned is cleared. Any failure leaves names untouched: a missing name is cosmetic and must never fail a listing.

package usagecache

import (
	"context"

	"github.com/tyclab/tycswap/internal/codex/api"
	"github.com/tyclab/tycswap/internal/codex/store"
)

// Plans that have a workspace name worth fetching (WORKSPACE_PLANS). A
// personal plan never does.
const (
	WorkspacePlansBusiness   = "business"
	WorkspacePlansEnterprise = "enterprise"
	WorkspacePlansEdu        = "edu"
)

func IsWorkspacePlan(plan string) bool {
	switch plan {
	case WorkspacePlansBusiness, WorkspacePlansEnterprise, WorkspacePlansEdu:
		return true
	}
	return false
}

func ScopeNeedsRefresh(scope []store.Slot) bool {
	if len(scope) <= 1 {
		return false
	}
	for _, s := range scope {
		if IsWorkspacePlan(s.Plan) && s.WorkspaceName == "" {
			return true
		}
	}
	return false
}

// payloadFor is the usage cache's callback, so this obeys the never-refresh-the-active-account rule.
func RefreshWorkspaceNames(ctx context.Context, st *store.Store, client api.Client, payloadFor PayloadFor) int {
	scopes := map[string][]store.Slot{}
	var order []string
	for _, s := range st.Slots() {
		if s.AuthMode == "apikey" {
			continue
		}
		uid := userID(s.AccountKey)
		if _, seen := scopes[uid]; !seen {
			order = append(order, uid)
		}
		scopes[uid] = append(scopes[uid], s)
	}

	changed := 0
	for _, uid := range order {
		scope := scopes[uid]
		if !ScopeNeedsRefresh(scope) {
			continue
		}

		// One request per scope. Any slot in it can ask on the scope's behalf,
		// so use the first with usable credentials.
		var names map[string]string
		for _, s := range scope {
			var payload map[string]any
			if payloadFor != nil {
				payload = payloadFor(s)
			}
			tokens, _ := payload["tokens"].(map[string]any)
			token, _ := tokens["access_token"].(string)
			acct, _ := tokens["account_id"].(string)
			if token == "" || acct == "" {
				continue
			}
			ws, err := client.FetchAccounts(ctx, token, acct)
			if err == nil {
				names = make(map[string]string, len(ws))
				for _, w := range ws {
					names[w.AccountID] = w.Name
				}
			}
			break
		}

		if len(names) == 0 {
			continue
		}

		for _, s := range scope {
			returned, ok := names[accountID(s.AccountKey)]
			switch {
			case ok:
				if returned != s.WorkspaceName && st.SetWorkspaceName(s.AccountKey, returned) == nil {
					changed++
				}
			case s.WorkspaceName != "" && IsWorkspacePlan(s.Plan):
				if st.SetWorkspaceName(s.AccountKey, "") == nil {
					changed++
				}
			}
		}
	}
	return changed
}
