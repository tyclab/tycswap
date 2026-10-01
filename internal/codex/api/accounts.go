// accounts.go — the /backend-api/accounts request behind Business, Enterprise
// and Edu workspace names. Ports the HTTP half of claude-swap PR #252
// codex/workspaces.py (the request itself lives in codex/usage.py as
// fetch_workspace_names).
//
// A personal ChatGPT account has no workspace name; a workspace account does,
// and it is what tells two of a user's workspaces apart in a listing. The name
// is not in the token — it only comes from this endpoint. WHEN to ask (the
// grouped-scope rules: one request per chatgpt_user_id scope, only when the
// scope has more than one record and an unnamed workspace plan) is deliberately
// not here; it sits beside the store, which owns the records it reasons about.
//
// A missing workspace name is cosmetic and must never fail a listing, so every
// failure is an error the caller answers by leaving stored names untouched —
// exactly as it treats an empty result.

package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/tyclab/tycswap/internal/codex/authfile"
)

// ErrMissingAuth is returned when there is no access token or account id to
// ask with; no request is made.
var ErrMissingAuth = errors.New("codex accounts: MissingAuth")

// Workspace is one named row of /backend-api/accounts. AccountID is the
// item's "id" (a chatgpt_account_id); Name its non-empty "name". Plan is the
// item's plan_type normalized through authfile.NormalizePlan when the row
// carries one, else "" — codex/workspaces.py does not read it, and nothing
// here depends on it.
type Workspace struct {
	AccountID, Name, Plan string
}

// FetchAccounts asks /backend-api/accounts for the calling user's workspaces.
// Rows with a null or empty name are omitted rather than returned as "", so a
// later successful fetch can still fill them in (storing "" would look like a
// real answer). A duplicated id keeps its position and takes the last name, as
// the Python dict it ports would.
func (c *HTTPClient) FetchAccounts(ctx context.Context, accessToken, accountID string) ([]Workspace, error) {
	if accessToken == "" || accountID == "" {
		return nil, ErrMissingAuth
	}
	data, err := c.getJSON(ctx, c.AccountsURL, accessToken, accountID)
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) {
			debugf("Codex account fetch failed: http-%d", se.Code)
		} else if errors.Is(err, errBadResponse) {
			debugf("Codex account fetch failed: bad-response")
		} else {
			debugf("Codex account fetch failed: %s", transportKind(err))
		}
		return nil, fmt.Errorf("codex accounts: %w", err)
	}
	ws, err := parseAccounts(data)
	if err != nil {
		debugf("Codex account fetch failed: bad-response")
		return nil, err
	}
	return ws, nil
}

// parseAccounts reads {"items": [{"id", "name", ...}]} into workspaces. A body
// without an items list is a bad response; items that are not objects, or lack
// a truthy id or a non-empty string name, are skipped.
func parseAccounts(data any) ([]Workspace, error) {
	doc, _ := data.(map[string]any)
	items, ok := doc["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("codex accounts: %w: no items list", errBadResponse)
	}
	out := []Workspace{}
	index := map[string]int{}
	for _, it := range items {
		item, ok := it.(map[string]any)
		if !ok {
			continue
		}
		id, name := item["id"], item["name"]
		nameStr, isStr := name.(string)
		if !truthy(id) || !isStr || nameStr == "" {
			continue
		}
		w := Workspace{AccountID: fmt.Sprint(id), Name: nameStr}
		if p, ok := item["plan_type"].(string); ok {
			w.Plan = authfile.NormalizePlan(p)
		}
		if i, seen := index[w.AccountID]; seen {
			out[i] = w
			continue
		}
		index[w.AccountID] = len(out)
		out = append(out, w)
	}
	return out, nil
}
