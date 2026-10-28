package routing

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
	"github.com/tyclab/tycswap/internal/usage"
)

type Request struct {
	ID      string `json:"id,omitempty"`
	Session string `json:"session"`
	Main    string `json:"main,omitempty"`
	Model   string `json:"model,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Launch  string `json:"launch,omitempty"`
	PID     int    `json:"pid"`
}

type Result struct {
	ID      string `json:"id,omitempty"`
	Account string `json:"account,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

type Coordinator struct{ Store *store.Store }

func validIdentity(r Request) error {
	if r.Session == "" || r.Launch == "" || r.PID <= 1 || !procdetect.IsPIDAlive(r.PID) {
		return fmt.Errorf("routing requires a live registered process and session")
	}
	return nil
}

func sessionFor(state *routingstate.State, group string, r Request) (routingstate.Session, error) {
	if err := validIdentity(r); err != nil {
		return routingstate.Session{}, err
	}
	session, ok := state.Sessions[routingstate.Key(group, r.Session)]
	if !ok || session.PID != r.PID || session.Launch != r.Launch {
		return routingstate.Session{}, fmt.Errorf("session is not registered for this routing process")
	}
	return session, nil
}

func (c Coordinator) Register(r Request) (Result, error) {
	if err := validIdentity(r); err != nil {
		return Result{}, err
	}
	if _, err := routingstate.Family(r.Model); err != nil {
		return Result{}, err
	}
	group := string(c.Store.GroupID())
	err := routingstate.With(c.Store.BackupDir(), func(state *routingstate.State) error {
		if existing, ok := state.Sessions[routingstate.Key(group, r.Session)]; ok && (existing.PID != r.PID || existing.Launch != r.Launch) {
			return fmt.Errorf("session is already registered by another process")
		}
		startup, starting := state.Leases[routingstate.Key(group, "startup-"+r.Launch)]
		if starting && (startup.PID != r.PID || startup.Session != r.Launch && startup.Session != r.Session) {
			return fmt.Errorf("routing launch reservation belongs to another session")
		}
		found := false
		for _, session := range state.Sessions {
			if session.Group == group && session.Launch == r.Launch && session.PID == r.PID {
				if session.ID != r.Launch && session.ID != r.Session {
					return fmt.Errorf("routing process already has a registered session")
				}
				if session.ID == r.Session && !starting {
					return nil
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("routing launch reservation is missing; start with tycswap claude")
		}
		for key, lease := range state.Leases {
			if lease.Group == group && lease.PID == r.PID {
				if lease.ID != "startup-"+r.Launch {
					return fmt.Errorf("the previous session has an unfinished request")
				}
				delete(state.Leases, key)
			}
		}
		for key, session := range state.Sessions {
			if session.Group == group && session.Launch == r.Launch && session.PID == r.PID {
				delete(state.Sessions, key)
			}
		}
		session := routingstate.Session{Group: group, ID: r.Session, PID: r.PID, Model: r.Model, Launch: r.Launch}
		state.Sessions[routingstate.Key(group, r.Session)] = session
		state.History[routingstate.Key(group, r.Session)] = session
		return nil
	})
	return Result{PID: r.PID}, err
}

func (c Coordinator) Unregister(r Request) (Result, error) {
	group := string(c.Store.GroupID())
	err := routingstate.With(c.Store.BackupDir(), func(state *routingstate.State) error {
		session, err := sessionFor(state, group, r)
		if err != nil {
			return err
		}
		for _, lease := range state.Leases {
			if lease.Group == group && lease.Session == r.Session {
				return fmt.Errorf("session has an unfinished request")
			}
		}
		delete(state.Sessions, routingstate.Key(group, r.Session))
		session.ID = r.Launch
		state.Sessions[routingstate.Key(group, r.Launch)] = session
		return nil
	})
	return Result{PID: r.PID}, err
}

func managedProfile(state *routingstate.State, s *store.Store) error {
	for _, session := range state.Sessions {
		if session.Group == string(s.GroupID()) && session.Launch == "" {
			return fmt.Errorf("profile has a Claude session outside automatic routing")
		}
	}
	pids, err := store.LiveProfilePIDs(s.ProfileDir())
	if err != nil {
		return err
	}
	native, err := procdetect.ListSessionsErr(s.ProfileDir())
	if err != nil {
		return err
	}
	allowed := map[int]bool{}
	seen := map[string]int{}
	for _, session := range native {
		registered, ok := state.Sessions[routingstate.Key(string(s.GroupID()), session.SessionID)]
		if !ok || registered.Launch == "" || runtime.GOOS != "windows" && registered.PID != session.PID {
			return fmt.Errorf("profile has a Claude session outside automatic routing")
		}
		if pid, exists := seen[session.SessionID]; exists && pid != session.PID {
			return fmt.Errorf("profile has several processes for the same conversation")
		}
		seen[session.SessionID] = session.PID
		allowed[session.PID] = true
	}
	for _, pid := range pids {
		if !allowed[pid] {
			return fmt.Errorf("profile has a process outside automatic routing")
		}
	}
	return nil
}

func (c Coordinator) Release(r Request) (Result, error) {
	if r.ID == "" {
		return Result{}, fmt.Errorf("request ID is required")
	}
	group := string(c.Store.GroupID())
	err := routingstate.With(c.Store.BackupDir(), func(state *routingstate.State) error {
		if _, err := sessionFor(state, group, r); err != nil {
			return err
		}
		key := routingstate.Key(group, r.ID)
		lease, ok := state.Leases[key]
		if !ok {
			return nil
		}
		if lease.Session != r.Session || lease.PID != r.PID {
			return fmt.Errorf("request belongs to another routing session")
		}
		delete(state.Leases, key)
		return nil
	})
	return Result{ID: r.ID}, err
}

func (c Coordinator) Acquire(r Request) (Result, error) {
	if err := validIdentity(r); err != nil {
		return Result{}, err
	}
	main, err := routingstate.Family(r.Main)
	if err != nil {
		return Result{}, err
	}
	model, err := routingstate.Family(r.Model)
	if err != nil {
		return Result{}, err
	}
	if r.ID == "" {
		return Result{}, fmt.Errorf("request ID is required")
	}
	if r.Agent != "" && model == "fable" && main != "fable" {
		return Result{}, fmt.Errorf("a Fable child requires a Fable main session")
	}
	group := string(c.Store.GroupID())
	if err := routingstate.With(c.Store.BackupDir(), func(state *routingstate.State) error {
		if _, err := sessionFor(state, group, r); err != nil {
			return err
		}
		return managedProfile(state, c.Store)
	}); err != nil {
		return Result{}, err
	}
	infos := reporting.BuildAccountsInfo(c.Store)
	fetch := make(map[string]bool, len(infos))
	for _, info := range infos {
		fetch[strconv.Itoa(info.Number)] = true
	}
	reporting.CollectUsageEntries(c.Store, infos, fetch)
	result := Result{ID: r.ID}
	err = routingstate.With(c.Store.BackupDir(), func(state *routingstate.State) error {
		session, err := sessionFor(state, group, r)
		if err != nil {
			return err
		}
		if err := managedProfile(state, c.Store); err != nil {
			return err
		}
		if old, ok := state.Leases[routingstate.Key(group, r.ID)]; ok {
			return fmt.Errorf("request ID is already in use by session %s", old.Session)
		}
		if r.Agent != "" {
			registered, err := routingstate.Family(session.Model)
			if err != nil || registered != main {
				return fmt.Errorf("child request main model no longer matches its session")
			}
		} else {
			previous, err := routingstate.Family(session.Model)
			if err != nil {
				return err
			}
			if previous != main {
				for _, lease := range state.Leases {
					if lease.Group == group && lease.Session == r.Session {
						return fmt.Errorf("finish the session's pending requests before changing its main model")
					}
				}
			}
			session.Model = r.Main
			state.Sessions[routingstate.Key(group, r.Session)] = session
		}
		models := map[string]bool{main: true, model: true}
		for _, other := range state.Sessions {
			if other.Group == group {
				family, err := routingstate.Family(other.Model)
				if err != nil {
					return err
				}
				models[family] = true
			}
		}
		for _, lease := range state.Leases {
			if lease.Group == group {
				family, err := routingstate.Family(lease.Model)
				if err != nil {
					return err
				}
				models[family] = true
			}
		}
		required := groups.Opus
		if models["fable"] {
			required = groups.Fable
		}
		s := c.Store.WithGroupIntent(groups.Continue).WithRoutingModel(required).WithRoutingLockHeld().WithGroupHandoff()
		data, err := s.ReadSequence()
		if err != nil || data == nil {
			return fmt.Errorf("account roster is unreadable")
		}
		entries := reporting.UsageEntriesByAccount(s, map[string]bool{})
		policy := settings.LoadOver(s.ScopeRoot(), settings.Load(s.BackupDir()))
		current := ""
		if num := s.CurrentAccountNumber(); num != nil {
			current = *num
		}
		var candidates []candidate
		transfers := map[string]*store.Store{}
		held := ""
		for _, lease := range state.Leases {
			if lease.Group == group {
				if held != "" && held != lease.Account {
					return fmt.Errorf("profile has conflicting routing requests")
				}
				held = lease.Account
			}
		}
		for _, number := range data.Sequence {
			num := strconv.Itoa(number)
			if held != "" && num != held {
				continue
			}
			if !s.RotationEligible(data, num) {
				source := transferable(state, s, data, num)
				if source == nil {
					continue
				}
				transfers[num] = source
			}
			score, ok := headroom(entries[num], clock.Seconds(s.Clk), models, policy)
			if !ok {
				continue
			}
			tier := 0
			if required != groups.Fable && s.WithRoutingModel(groups.Fable).GroupCompatibility(num, groups.Continue).Allowed {
				tier = 1
			}
			candidates = append(candidates, candidate{num: num, tier: tier, score: score})
		}
		target := choose(candidates, current, policy.HysteresisPct)
		if target == "" {
			return fmt.Errorf("no available account has fresh quota for the requested models")
		}
		if err := state.CheckSwitch(group, target); err != nil {
			return err
		}
		if source := transfers[target]; source != nil {
			if _, err := switching.ReleaseGroup(source, false); err != nil {
				return err
			}
		}
		if _, err := switching.SwitchTo(s, target, true, false); err != nil {
			return err
		}
		if actual := s.CurrentAccountNumber(); actual == nil || *actual != target {
			return fmt.Errorf("selected account could not be verified")
		}
		state.Leases[routingstate.Key(group, r.ID)] = routingstate.Lease{ID: r.ID, Group: group, Session: r.Session, Account: target, Model: r.Model, Agent: r.Agent, PID: r.PID}
		state.History[routingstate.Key(group, r.Session)] = session
		result.Account = target
		return nil
	})
	return result, err
}

func transferable(state *routingstate.State, s *store.Store, data *store.SequenceData, number string) *store.Store {
	owner, err := s.CredentialOwner(number)
	if err != nil || owner.Uncertain || owner.Scope == string(s.GroupID()) {
		return nil
	}
	id, err := groups.Parse(owner.Scope)
	if err != nil || state.CheckIdle(owner.Scope) != nil {
		return nil
	}
	source, err := s.ForGroup(id)
	if err != nil || !source.RotationEligible(data, number) {
		return nil
	}
	pids, err := store.LiveProfilePIDs(source.ProfileDir())
	if err != nil || len(pids) != 0 {
		return nil
	}
	return source
}

type candidate struct {
	num   string
	tier  int
	score float64
}

func choose(candidates []candidate, current string, hysteresis float64) string {
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	var active *candidate
	for i := range candidates {
		row := &candidates[i]
		if row.tier < best.tier || (row.tier == best.tier && row.score > best.score) {
			best = *row
		}
		if row.num == current {
			active = row
		}
	}
	if active != nil && active.tier == best.tier && best.score-active.score <= hysteresis {
		return active.num
	}
	return best.num
}

func headroom(entry usage.UsageEntry, now float64, models map[string]bool, policy settings.AutoSwitchSettings) (float64, bool) {
	if !entry.Fresh(now, usage.ServeTTLS) || entry.FetchedAt == nil || *entry.FetchedAt > now || entry.Sentinel != "" || entry.TokenDead() {
		return 0, false
	}
	u := oauth.NewUsage(entry.LastGood)
	if u == nil || u.FiveHour == nil || u.SevenDay == nil || u.FiveHour.Pct < 0 || u.SevenDay.Pct < 0 {
		return 0, false
	}
	if u.FiveHour.Pct >= policy.FiveHourThreshold || u.SevenDay.Pct >= policy.SevenDayThreshold {
		return 0, false
	}
	max := u.FiveHour.Pct
	if u.SevenDay.Pct > max {
		max = u.SevenDay.Pct
	}
	fable := false
	expected := 0
	if raw, exists := entry.LastGood["scoped"]; exists {
		windows, ok := raw.([]any)
		if !ok {
			return 0, false
		}
		for _, rawWindow := range windows {
			window, ok := rawWindow.(map[string]any)
			if !ok {
				return 0, false
			}
			name, ok := window["name"].(string)
			if !ok || name == "" {
				return 0, false
			}
			if models[strings.ToLower(name)] {
				expected++
			}
		}
	}
	for _, scoped := range u.Scoped {
		name := strings.ToLower(scoped.Name)
		if !models[name] {
			continue
		}
		expected--
		if name == "fable" {
			fable = true
		}
		if scoped.Pct < 0 || scoped.Pct >= policy.ModelThreshold {
			return 0, false
		}
		if scoped.Pct > max {
			max = scoped.Pct
		}
	}
	return 100 - max, expected == 0 && (!models["fable"] || fable)
}
