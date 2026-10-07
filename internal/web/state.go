// state.go — the GET /api/state document: accounts (jsonout usage + at-limit
// fields, exactly as `tycswap list --json` serialises them, optionally enriched
// with the token status), sessions, settings, auto-switch engine, strategies,
// server time, and (A27) the live login, the auth overrides, the updates and
// the page's view choices.
//
// Implements DESIGN A26 "API. GET /api/state → one State document". Account
// rows are maps because the additive at-limit / freshness / tokenStatus keys
// are present only when set (schemaVersion-1 optional-key discipline,
// jsonout); every other section is a tagged struct so the shape is fixed at
// compile time. Optional façades that are nil serialise as null.
package web

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/version"
)

// stateSchemaVersion is the dashboard state document's own schema version.
const stateSchemaVersion = 1

// State is the document GET /api/state returns and every SSE state event
// carries.
type State struct {
	Sequence      uint64           `json:"sequence"`
	SchemaVersion int              `json:"schemaVersion"`
	ServerTime    string           `json:"serverTime"`
	Version       string           `json:"version"`
	ActiveNumber  any              `json:"activeNumber"` // int, or nil when no managed account is active
	Accounts      []map[string]any `json:"accounts"`
	Sessions      SessionsJSON     `json:"sessions"`
	Groups        []GroupView      `json:"groups,omitempty"`
	Recovery      *RecoveryView    `json:"recovery,omitempty"`
	Settings      []SettingView    `json:"settings"` // null when no SettingsFacade
	Auto          *AutoView        `json:"auto"`     // null when no AutoFacade
	Strategies    []string         `json:"strategies"`
	Name          string           `json:"name"` // brand.Name, for the command hints the page prints
	// CurrentLogin is the account Claude Code is signed in with; null when
	// the server cannot tell (no Deps.CurrentLogin) or Claude Code has none
	// (A27).
	CurrentLogin *CurrentLoginView `json:"currentLogin"`
	// AuthOverrides: what makes Claude Code ignore its stored login (A27).
	AuthOverrides AuthOverridesView `json:"authOverrides"`
	// Updates: what the host found to update; null when no UpdatesFacade (A27).
	Updates *UpdatesView `json:"updates"`
	// UI: the page's remembered view choices; null without Deps.UIPrefs (A27).
	UI *UIView `json:"ui"`
}

// SessionsJSON is the sessions section.
type SessionsJSON struct {
	Claude []ClaudeSessionJSON `json:"claude"`
	IDE    []IdeInstanceJSON   `json:"ide"`
}

// ClaudeSessionJSON is one running Claude Code session.
type ClaudeSessionJSON struct {
	PID        int     `json:"pid"`
	SessionID  string  `json:"sessionId"`
	CWD        string  `json:"cwd"`
	StartedAt  int64   `json:"startedAt"` // epoch milliseconds
	Kind       string  `json:"kind"`
	Entrypoint string  `json:"entrypoint"`
	Status     *string `json:"status"`
	Title      string  `json:"title"`   // from the transcript; "" when none yet
	Profile    string  `json:"profile"` // slot of the `run`/`env` session profile; "" for the default login
	Group      string  `json:"group,omitempty"`
	Model      string  `json:"model,omitempty"`
}

// IdeInstanceJSON is one running IDE instance.
type IdeInstanceJSON struct {
	Port             int      `json:"port"`
	PID              int      `json:"pid"`
	IDEName          string   `json:"ideName"`
	WorkspaceFolders []string `json:"workspaceFolders"`
}

// stateOpts are the per-request knobs on the state document.
type stateOpts struct {
	// tokenStatus lifts each row's OAuth token status out of the
	// `list --token-status --json` payload (AccountOps.ListAccounts).
	tokenStatus bool
}

// buildState takes one snapshot (nil fetch set: every stale account is
// eligible, the store paces the network exactly as the TUI's poll tick does)
// and assembles the document. With a merged source the Codex rows follow the
// Claude rows; activeNumber, currentLogin and the token status stay Claude's.
func (s *Server) buildState(o stateOpts) State {
	seq := s.stateSeq.Add(1)
	snap := s.d.Snapshot.AccountsSnapshot(nil)
	st := State{
		Sequence:      seq,
		SchemaVersion: stateSchemaVersion,
		ServerTime:    s.d.Clock.Now().UTC().Format(time.RFC3339),
		Version:       version.Display(),
		Accounts:      []map[string]any{},
		Sessions:      SessionsJSON{Claude: []ClaudeSessionJSON{}, IDE: []IdeInstanceJSON{}},
		Strategies:    append([]string(nil), Strategies...),
		Name:          brand.Sanitized().Name,
	}
	if snap != nil {
		if n, err := strconv.Atoi(snap.ActiveNumber); err == nil {
			st.ActiveNumber = n
		}
		for _, a := range snap.Accounts {
			st.Accounts = append(st.Accounts, accountRow(a))
		}
	}
	if o.tokenStatus && s.d.Accounts != nil {
		s.enrichTokenStatus(st.Accounts)
	}
	if s.d.CurrentLogin != nil {
		if email, ok := s.d.CurrentLogin(); ok && email != "" {
			// Saved is exactly "the active account is managed": the
			// snapshot's active number is the stored slot of the live login.
			st.CurrentLogin = &CurrentLoginView{Email: email, Saved: st.ActiveNumber != nil}
		}
	}
	st.AuthOverrides = s.d.AuthOverrides()
	// tycswap's endpoint profile is the login while the active account is the
	// one with a base URL; under any other live login (a /login made while it
	// was active, say) it overrides that login, and the notice says so.
	if len(st.AuthOverrides.Profile) > 0 && !activeHasBaseURL(snap) {
		st.AuthOverrides.Settings = append(append([]string(nil), st.AuthOverrides.Settings...), st.AuthOverrides.Profile...)
		sort.Strings(st.AuthOverrides.Settings)
	}
	if st.AuthOverrides.Env == nil {
		st.AuthOverrides.Env = []string{}
	}
	if st.AuthOverrides.Settings == nil {
		st.AuthOverrides.Settings = []string{}
	}
	if s.d.Updates != nil {
		st.Updates = s.updatesView()
	}
	if s.d.UIPrefs != nil {
		st.UI = s.uiView()
	}

	if s.d.Settings != nil {
		st.Settings = s.settingsViews()
	}
	if s.d.Groups != nil {
		st.Groups = s.d.Groups.Views()
	}
	if s.d.Recovery != nil && snap != nil {
		v := s.d.Recovery.View(*snap)
		st.Recovery = &v
	}
	if s.d.Auto != nil {
		v := s.d.Auto.View()
		if v.Events == nil {
			v.Events = []AutoEventView{}
		}
		st.Auto = &v
	}
	sv := s.d.Sessions()
	for _, c := range sv.Claude {
		item := s.claudeSessionJSON(c, sv.ConfigDir[c.PID], sv.Profile[c.PID])
		if st.Recovery != nil {
			item.Model = st.Recovery.SessionModels[c.SessionID]
		}
		st.Sessions.Claude = append(st.Sessions.Claude, item)
	}
	for _, i := range sv.IDE {
		folders := i.WorkspaceFolders
		if folders == nil {
			folders = []string{}
		}
		st.Sessions.IDE = append(st.Sessions.IDE, IdeInstanceJSON{
			Port: i.Port, PID: i.PID, IDEName: i.IDEName, WorkspaceFolders: folders,
		})
	}
	return st
}

// stateJSON is buildState marshalled.
func (s *Server) stateJSON(o stateOpts) ([]byte, error) {
	return json.Marshal(s.buildState(o))
}

// enrichTokenStatus adds "tokenStatus" to every Claude row from the
// `list --token-status --json` payload, matched by account number. The listing
// is store-only (empty, non-nil fetch set): the snapshot just paced the
// network. A row the payload does not describe gets "" so the key is always
// present when asked for.
func (s *Server) enrichTokenStatus(rows []map[string]any) {
	byNum := map[string]string{}
	payload, err := s.d.Accounts.ListAccounts(true, true, map[string]bool{})
	if err != nil {
		s.d.Logger("web: token status: " + err.Error())
	} else if m, ok := payload.(map[string]any); ok {
		if list, ok := m["accounts"].([]any); ok {
			for _, item := range list {
				row, ok := item.(map[string]any)
				if !ok {
					continue
				}
				ts, _ := row["tokenStatus"].(string)
				byNum[fmt.Sprint(row["number"])] = ts
			}
		}
	}
	for _, r := range rows {
		if r["provider"] != reporting.ProviderClaude {
			continue
		}
		r["tokenStatus"] = byNum[fmt.Sprint(r["number"])]
	}
}

// activeHasBaseURL reports whether the snapshot's active Claude account is
// an API-key account with a base URL (DESIGN A46). A Codex row's active flag
// is about another CLI's login and says nothing here.
func activeHasBaseURL(snap *reporting.AccountsSnapshot) bool {
	if snap == nil {
		return false
	}
	for _, a := range snap.Accounts {
		if a.ProviderName() != reporting.ProviderClaude {
			continue
		}
		if a.IsActive {
			return a.BaseURL != ""
		}
	}
	return false
}

// accountRow projects one AccountSnapshot. The usage value is the
// decision-grade one (`DecisionValue`), so a measurement older than STALE_OK_S
// reports "unavailable" exactly as `tycswap list --json` does. provider and
// key ("<provider>:<number>", AccountSnapshot.Key) tell rows of different
// providers apart, whose slot numbers overlap; every account route addresses
// a row by its key.
func accountRow(a reporting.AccountSnapshot) map[string]any {
	dv := a.Usage.DecisionValue()
	status, usage := jsonout.UsageFields(dv)
	var number any = a.Number
	if n, err := strconv.Atoi(a.Number); err == nil {
		number = n
	}
	row := map[string]any{
		"number":           number,
		"email":            a.Email,
		"alias":            a.Alias,
		"orgName":          a.OrgName,
		"kind":             a.Kind,
		"isActive":         a.IsActive,
		"disabled":         a.Disabled,
		"switchable":       a.Switchable,
		"rotationEligible": a.RotationEligible,
		"usageStatus":      status,
		"provider":         a.ProviderName(),
		"key":              a.Key(),
	}
	if usage == nil {
		row["usage"] = nil
	} else {
		row["usage"] = usage
	}
	if a.BaseURL != "" {
		// The endpoint an API-key account sends its requests to (DESIGN
		// A46); the page shows its host.
		row["baseUrl"] = a.BaseURL
	}
	for k, v := range jsonout.AtLimitFields(a.AtLimit, a.LimitingWindows) {
		row[k] = v
	}
	if usage != nil {
		for k, v := range jsonout.UsageFreshnessFields(a.Usage.FetchedAt, a.Usage.AgeS) {
			row[k] = v
		}
	}
	refresh := map[string]any{}
	if a.Usage.LastError != "" {
		refresh["error"] = a.Usage.LastError
	}
	next := a.Usage.NextPollAt
	if a.Usage.BackoffUntil != nil && (next == nil || *a.Usage.BackoffUntil > *next) {
		next = a.Usage.BackoffUntil
	}
	if next != nil {
		refresh["nextAt"] = time.Unix(int64(*next), 0).UTC().Format(time.RFC3339)
	}
	if len(refresh) > 0 {
		row["usageRefresh"] = refresh
	}
	return row
}

func (s *Server) claudeSessionJSON(c procdetect.ClaudeSession, configDir, profile string) ClaudeSessionJSON {
	group := ""
	if profile == "fable" || profile == "opus" {
		group, profile = profile, ""
	}
	return ClaudeSessionJSON{
		PID:        c.PID,
		SessionID:  c.SessionID,
		CWD:        c.CWD,
		StartedAt:  c.StartedAt,
		Kind:       c.Kind,
		Entrypoint: c.Entrypoint,
		Status:     c.Status,
		Title:      s.d.SessionTitle(configDir, c.CWD, c.SessionID),
		Profile:    profile,
		Group:      group,
	}
}

// withoutTokenStatus is st with every row's tokenStatus key dropped (rows are
// copied; st's own rows are left alone).
func withoutTokenStatus(st State) State {
	rows := make([]map[string]any, len(st.Accounts))
	for i, r := range st.Accounts {
		c := make(map[string]any, len(r))
		for k, v := range r {
			if k != "tokenStatus" {
				c[k] = v
			}
		}
		rows[i] = c
	}
	st.Accounts = rows
	return st
}
