// fable.go — the Fable store on the read surfaces (DESIGN A104): list's
// fableAccountNumber and fableLogin, the fable object status and the
// dashboard carry, and their human lines. Nothing here exists while no
// account has a Fable login.
package reporting

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/jsonout"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/printer"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
	"github.com/tyclab/tycswap/internal/usage"
)

// FableLogin is an account's Fable login as the surfaces show it: Live
// while the Fable store holds it; Status FableLoginOK, or FableSignInAgain
// for a login the Fable engine quarantined on invalid_grant.
type FableLogin struct {
	Live   bool   `json:"live"`
	Status string `json:"status"`
}

const (
	FableLoginOK     = "ok"
	FableSignInAgain = "sign in again"
)

// fableView is the Fable store as the read surfaces show it. Its live slot
// is resolved as BuildAccountsInfo resolves it, without writing the Fable
// chain record.
type fableView struct {
	state  store.FableState
	live   store.LiveSlot
	logins map[string]FableLogin
}

// readFableView is nil while no account has a Fable login, and on a Fable
// scope.
func readFableView(s *store.Store, data *store.SequenceData) *fableView {
	if data == nil || s.IsFable() || !s.HasFableLogins() {
		return nil
	}
	f, err := s.ForFable()
	if err != nil {
		return nil
	}
	v := &fableView{state: s.FableStore(), logins: map[string]FableLogin{}}
	if v.state.On {
		v.live = s.WithoutLineageWrites().ResolveFableLiveSlot(data)
	}
	held := autoswitch.ReadQuarantineEntries(f.StatePath())
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if !s.HasFableLogin(num) {
			continue
		}
		login := FableLogin{Live: num == v.live.Slot, Status: FableLoginOK}
		if held[num].Reason == oauth.ErrInvalidGrant {
			login.Status = FableSignInAgain
		}
		v.logins[num] = login
	}
	return v
}

// login is num's Fable login, nil without one.
func (v *fableView) login(num string) *FableLogin {
	if v == nil {
		return nil
	}
	if l, ok := v.logins[num]; ok {
		return &l
	}
	return nil
}

// accountNumber is fableAccountNumber: the live slot, nil when the store is
// off or its slot is unknown.
func (v *fableView) accountNumber() any {
	if n, err := strconv.Atoi(v.live.Slot); err == nil {
		return n
	}
	return nil
}

func (v *fableView) stateName() string {
	switch {
	case !v.state.On:
		return "off"
	case !v.state.Usable:
		return "unusable"
	}
	return "on"
}

// object is the fable object of status --json and the dashboard's state:
// the live account's fields in the shapes of status's active object, from
// entry, its shared usage row.
func (v *fableView) object(s *store.Store, data *store.SequenceData, entry usage.UsageEntry) map[string]any {
	obj := map[string]any{
		"state":    v.stateName(),
		"storeDir": filepath.Clean(paths.FableStoreDir(s.BackupDir())),
		"number":   v.accountNumber(),
		"source":   nil,
	}
	if v.state.Reason != "" {
		obj["reason"] = v.state.Reason
	}
	slot := v.live.Slot
	if slot == "" {
		return obj
	}
	rec, _ := recordFor(data, slot)
	obj["email"], obj["organizationName"], obj["source"] = recStr(rec, "email"), recStr(rec, "organizationName"), v.live.Source
	if plan := s.RecordPlan(slot, rec); plan != "" {
		obj["plan"] = plan
	}
	status, usageJSON := jsonout.UsageFields(entry.DecisionValue())
	obj["usageStatus"] = status
	if usageJSON == nil {
		obj["usage"] = nil
	} else {
		obj["usage"] = usageJSON
	}
	return obj
}

// line is status's one human line for the Fable store.
func (v *fableView) line(data *store.SequenceData, entry usage.UsageEntry) string {
	head := printer.Bolded("Fable store:") + " "
	switch {
	case !v.state.On:
		return head + "off"
	case v.live.Slot == "":
		return head + "on, its account unknown"
	}
	rec, _ := recordFor(data, v.live.Slot)
	line := head + printer.Accent("Account-"+v.live.Slot) + " (" + termsafe.Strip(recStr(rec, "email")) + ")"
	if !v.state.Usable {
		return line + " · " + printer.Yellowed("Fable login: sign in again")
	}
	value, _ := entry.DecisionValue().(map[string]any)
	if pct, ok := fableWindowPct(value); ok {
		line += fmt.Sprintf(" · Fable %.0f%%", pct)
	}
	if seven, ok := value["seven_day"].(map[string]any); ok {
		if pct, ok := asFloat(seven["pct"]); ok {
			line += fmt.Sprintf(" · 7d %.0f%%", pct)
		}
	}
	return line
}

// fableWindowPct is the fullest Fable window a usage value reports.
func fableWindowPct(value map[string]any) (float64, bool) {
	scoped, _ := value["scoped"].([]any)
	best, found := 0.0, false
	for _, item := range scoped {
		w, _ := item.(map[string]any)
		name, _ := w["name"].(string)
		pct, ok := asFloat(w["pct"])
		if ok && strings.HasPrefix(strings.ToLower(name), "fable") && (!found || pct > best) {
			best, found = pct, true
		}
	}
	return best, found
}

// fableTag is list's tag for a row: the Fable store's account, then its
// login's state; "" for a row without a Fable login.
func fableTag(login *FableLogin) string {
	if login == nil {
		return ""
	}
	var tags []string
	if login.Live {
		tags = append(tags, printer.BoldAccent("(Fable store)"))
	}
	switch {
	case login.Status == FableSignInAgain:
		tags = append(tags, printer.Yellowed("(Fable login: sign in again)"))
	case !login.Live:
		tags = append(tags, printer.Muted("(Fable login)"))
	}
	return strings.Join(tags, " ")
}

// fableInfo is the Fable store's live account's info row, for a pass of the
// collector over that account alone; false when it has none.
func fableInfo(s *store.Store, v *fableView) (AccountInfo, bool) {
	if v == nil || v.live.Slot == "" {
		return AccountInfo{}, false
	}
	for _, info := range BuildAccountsInfo(s) {
		if strconv.Itoa(info.Number) == v.live.Slot {
			info.FableLive = true
			return info, true
		}
	}
	return AccountInfo{}, false
}

// fableStatus is the fable object and the human line for status: the live
// account's usage comes from its shared row through the collector, which
// fetches with the account's default login, never with the Fable one.
func fableStatus(s *store.Store) (map[string]any, string, bool) {
	data, _ := s.SequenceMigrated()
	v := readFableView(s, data)
	if v == nil {
		return nil, "", false
	}
	var entry usage.UsageEntry
	if info, ok := fableInfo(s, v); ok {
		entry = CollectUsageEntries(s, []AccountInfo{info}, nil)[v.live.Slot]
	}
	return v.object(s, data, entry), v.line(data, entry), true
}
