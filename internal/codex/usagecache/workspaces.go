// workspaces.go — refresh Business/Enterprise/Edu workspace names. Implements
// the grouped-scope rules of claude-swap PR #252 codex/workspaces.py; the
// request itself is api.Client.FetchAccounts.
//
// A personal ChatGPT account has no workspace name; a Business, Enterprise or
// Edu one does, and it is what tells two of a user's workspaces apart in a
// listing. The name is not in the token — it only comes from
// /backend-api/accounts.
//
// The point of this file is not fetching, it is *not* fetching. Asking for
// workspace names on every listing would add a request per user scope to every
// `tycswap codex list`, forever, to fill a field that changes approximately
// never. So codex-auth's grouped-scope rules (its docs/api.md) are followed
// exactly:
//
//   - A scope is all stored slots sharing one chatgpt_user_id. One user can
//     hold several workspaces; one request answers for all of them.
//   - A request is attempted only when the scope holds more than one record,
//     at least one of them is a workspace plan, and at least one such record
//     still has no name. A single personal account therefore never asks.
//   - At most one request per scope per pass.
//   - Matched records overwrite the stored name even when they already had
//     one — the server is authoritative, and a renamed workspace should follow.
//   - In-scope workspace-plan records the response does not return are
//     cleared back to empty: a stale name for a workspace the user has lost
//     access to is worse than none.
//   - Any failure is non-fatal and leaves stored names untouched. A missing
//     workspace name is cosmetic; it must never fail a listing.

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

// IsWorkspacePlan reports whether plan is one of the workspace plans.
func IsWorkspacePlan(plan string) bool {
	switch plan {
	case WorkspacePlansBusiness, WorkspacePlansEnterprise, WorkspacePlansEdu:
		return true
	}
	return false
}

// ScopeNeedsRefresh reports whether one chatgpt_user_id scope justifies a
// request: more than one record, at least one workspace plan, and at least one
// such record without a name.
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

// RefreshWorkspaceNames fills in missing workspace names and returns how many
// names changed. payloadFor is the same callback the usage cache takes, so
// this obeys the never-refresh-the-active-account rule without knowing about
// it. Store write errors are skipped (the name is cosmetic) and not counted.
func RefreshWorkspaceNames(ctx context.Context, st *store.Store, client api.Client, payloadFor PayloadFor) int {
	// Scopes in first-seen order, so the request order is deterministic.
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
			// Includes the failure case: leave every stored name untouched.
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
				// In scope, a workspace plan, and not returned: access is gone.
				if st.SetWorkspaceName(s.AccountKey, "") == nil {
					changed++
				}
			}
		}
	}
	return changed
}
