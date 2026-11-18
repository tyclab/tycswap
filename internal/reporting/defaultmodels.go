package reporting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/usage"
)

const (
	defaultModelsFile = "default_models.json"
	// The status line reruns every few seconds; an unchanged model is
	// rewritten at most this often, a changed one at once.
	defaultModelRefresh = time.Minute
	// Records of sessions that no longer run are dropped after a day.
	defaultModelKeep = 24 * time.Hour
)

type defaultSessionModel struct {
	Family string  `json:"family"`
	SeenAt float64 `json:"seenAt"`
	// Store is "fable" for a session on the Fable store, "" for the default
	// store's (DESIGN A100).
	Store string `json:"store,omitempty"`
}

// RecordedSession is the model family and store recorded for a session, ok
// false when it has no record.
func RecordedSession(root, sessionID string) (family, store string, ok bool) {
	rec, ok := readDefaultSessionModels(root)[sessionID]
	return rec.Family, rec.Store, ok
}

// SessionStores is each recorded session's store, "fable" or "" for the
// default store; a session without a record is not in it.
func SessionStores(root string) map[string]string {
	out := map[string]string{}
	for id, rec := range readDefaultSessionModels(root) {
		out[id] = rec.Store
	}
	return out
}

func readDefaultSessionModels(root string) map[string]defaultSessionModel {
	var file struct {
		Sessions map[string]defaultSessionModel `json:"sessions"`
	}
	if raw, err := os.ReadFile(filepath.Join(root, defaultModelsFile)); err == nil {
		_ = json.Unmarshal(raw, &file)
	}
	if file.Sessions == nil {
		file.Sessions = map[string]defaultSessionModel{}
	}
	return file.Sessions
}

// ModelFamily is the family of a Claude model id or alias ("fable", "opus",
// "sonnet", "haiku"), "" for anything else.
func ModelFamily(model string) string {
	m := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "[1m]")
	for _, ch := range m {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
			return ""
		}
	}
	for _, family := range []string{"fable", "opus", "sonnet", "haiku"} {
		if m == family || strings.HasPrefix(m, "claude-"+family+"-") && len(m) > len("claude-"+family+"-") {
			return family
		}
	}
	return ""
}

// RecordDefaultSessionModel remembers the model family a session runs, as its
// status line reports it (DESIGN A76), and its store: "fable" for a session on
// the Fable store, "" for the default store's (A102). It reports whether that
// was news: a session seen for the first time or one that changed family or
// store (A77). An unknown model or a busy lock records nothing.
func RecordDefaultSessionModel(root, sessionID, model, store string, now time.Time) (bool, error) {
	family := ModelFamily(model)
	if sessionID == "" || family == "" {
		return false, nil
	}
	lock := filelock.New(filepath.Join(root, ".default_models.lock"), 50*time.Millisecond)
	if ok, err := lock.Acquire(0); err != nil || !ok {
		return false, err
	}
	defer lock.Release()
	sessions := readDefaultSessionModels(root)
	at := float64(now.UnixNano()) / 1e9
	old, seen := sessions[sessionID]
	changed := !seen || old.Family != family || old.Store != store
	if !changed && at-old.SeenAt < defaultModelRefresh.Seconds() && at >= old.SeenAt {
		return false, nil
	}
	sessions[sessionID] = defaultSessionModel{Family: family, SeenAt: at, Store: store}
	for id, rec := range sessions {
		if at-rec.SeenAt > defaultModelKeep.Seconds() {
			delete(sessions, id)
		}
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "sessions": sessions})
	if err != nil {
		return false, err
	}
	return changed, atomicfile.Write(filepath.Join(root, defaultModelsFile), raw, atomicfile.Opts{})
}

// PolicyResult is the default-session policy for one tick (DESIGN A76, A77,
// A91): the settings to decide with, the accounts left to Fable work (nil
// reserves none), the derived models dropped because no usable account can
// serve them, and the model families the live default sessions run.
type PolicyResult struct {
	Settings   settings.AutoSwitchSettings
	Reserved   func(num string) bool
	Unservable []string
	Sessions   []string
}

// DefaultSessionPolicy is DefaultSessionPolicyResult at the store's clock.
func DefaultSessionPolicy(s *store.Store, base settings.AutoSwitchSettings) (settings.AutoSwitchSettings, func(num string) bool) {
	r := DefaultSessionPolicyResult(s, base, s.Clk.Now())
	return r.Settings, r.Reserved
}

// DefaultSessionPolicyResult counts, for the default login, the model limits
// of the families its live sessions run (DESIGN A76), but only those some
// usable account can serve (A91). While every live session has a recorded
// model, none of them Fable, it reserves the accounts that can run Fable and
// still have Fable room (A77, A91). It always decides: no setting or flag
// names the counted models (A97). A live session of unknown model reserves
// nothing.
//
// While the Fable store is usable (A102) it counts the default store's
// sessions only, those whose record names no store, and always reserves the
// Fable pool's accounts with Fable room and the Fable store's live account.
func DefaultSessionPolicyResult(s *store.Store, base settings.AutoSwitchSettings, now time.Time) PolicyResult {
	base.Model = nil
	r := PolicyResult{Settings: base}
	fable := s.FableStore()
	var cached *policyView
	viewOf := func() *policyView {
		if cached == nil {
			v := newPolicyView(s, now)
			cached = &v
		}
		return cached
	}
	if fable.Usable {
		r.Reserved = fableReserved(s, fable.Live.Slot, viewOf().fableBelow(base.ModelThreshold))
	}
	records := readDefaultSessionModels(s.BackupDir())
	if len(records) == 0 {
		return r
	}
	sessions, _, err := procdetect.GetRunningInstancesErr(s.DefaultProfileDir())
	if err != nil || len(sessions) == 0 {
		return r
	}
	families := map[string]bool{}
	allKnown := true
	for _, session := range sessions {
		rec, ok := records[session.SessionID]
		if !ok {
			allKnown = false
			continue
		}
		if fable.Usable && rec.Store == "fable" {
			continue
		}
		families[rec.Family] = true
	}
	if len(families) == 0 {
		return r
	}
	for family := range families {
		r.Sessions = append(r.Sessions, family)
	}
	sort.Strings(r.Sessions)
	view := viewOf()
	var counted []string
	for _, name := range scopedNamesFor(view.usage, families) {
		if view.serves(name, base) {
			counted = append(counted, name)
		} else {
			r.Unservable = append(r.Unservable, name)
		}
	}
	if model := strings.Join(counted, ","); model != "" {
		r.Settings.Model = &model
	}
	if fable.Usable || !allKnown || families["fable"] {
		return r
	}
	room := view.fableBelow(base.ModelThreshold)
	r.Reserved = func(num string) bool { return room[num] && s.FableCapable(num) }
	return r
}

// fableReserved is the default store's reservation beside a usable Fable
// store (DESIGN A102): the Fable pool's accounts with Fable room, and the
// Fable store's live account whatever its room. An account the Fable store
// cannot use is left to the default store.
func fableReserved(s *store.Store, live string, room map[string]bool) func(num string) bool {
	pool := map[string]bool{}
	for _, num := range FablePool(s) {
		pool[num] = room[num]
	}
	return func(num string) bool { return num != "" && num == live || pool[num] }
}

// FablePool is the accounts the Fable store may use (DESIGN A102): enabled
// and rotation-eligible by the default rules, able to run Fable, holding a
// Fable login, and not quarantined in the Fable state file. s is the
// default store.
func FablePool(s *store.Store) []string {
	f, err := s.ForFable()
	data, _ := s.ReadSequence()
	if err != nil || data == nil {
		return nil
	}
	held := autoswitch.ReadQuarantineEntries(f.StatePath())
	var out []string
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if _, dead := held[num]; dead || !s.RotationEligible(data, num) || !s.FableCapable(num) || !s.HasFableLogin(num) {
			continue
		}
		out = append(out, num)
	}
	return out
}

// FableWindowNames is every Fable window name an account's cached usage
// reports, sorted: what the Fable store's tick counts (DESIGN A102).
func FableWindowNames(s *store.Store, now time.Time) []string {
	return scopedNamesFor(newPolicyView(s, now).usage, map[string]bool{"fable": true})
}

// policyView is every account's cached usage, its usage the engine would
// still decide on (serving), and the pool a tick may use: enabled,
// rotation-eligible, not quarantined, plus the live account. Both readings
// are taken as of now, as the engine takes them (oauth.AsOf).
type policyView struct {
	usage   map[string]*oauth.Usage
	serving map[string]*oauth.Usage
	pool    map[string]bool
}

func newPolicyView(s *store.Store, now time.Time) policyView {
	s = passStore(s, map[string]bool{})
	infos := BuildAccountsInfo(s)
	entries := CollectUsageEntries(s, infos, map[string]bool{})
	data, _ := s.ReadSequence()
	quarantined := autoswitch.ReadQuarantineEntries(autoswitch.StatePath(s.BackupDir()))
	v := policyView{usage: map[string]*oauth.Usage{}, serving: map[string]*oauth.Usage{}, pool: map[string]bool{}}
	for _, info := range infos {
		num := strconv.Itoa(info.Number)
		entry := entries[num]
		if u := oauth.NewUsage(oauth.AsOf(entry.LastGood, now)); u != nil {
			v.usage[num] = u
		}
		if u := oauth.NewUsage(oauth.AsOf(servingValue(entry), now)); u != nil {
			v.serving[num] = u
		}
		_, held := quarantined[num]
		v.pool[num] = info.IsActive || !held && s.RotationEligible(data, num)
	}
	return v
}

// servingValue is the reading an account may serve a model on: the one the
// engine still decides on (UsageEntry.DecisionValue: younger than
// usage.StaleOKS, or trusted up to usage.TrustMaxAgeS, also after failed
// fetches). A sentinel or an untrusted reading is no map and serves nothing.
func servingValue(e usage.UsageEntry) map[string]any {
	m, _ := e.DecisionValue().(map[string]any)
	return m
}

// serves reports whether an account in the pool sits below the bars the
// engine moves at on model's window and on its 5h and 7d windows; an absent
// 5h window is room, an absent 7d window is not.
func (v policyView) serves(model string, bars settings.AutoSwitchSettings) bool {
	for num, u := range v.serving {
		if !v.pool[num] || u.SevenDay == nil || u.SevenDay.Pct >= bars.SevenDayThreshold ||
			u.FiveHour != nil && u.FiveHour.Pct >= bars.FiveHourThreshold {
			continue
		}
		for _, w := range u.Scoped {
			if strings.EqualFold(w.Name, model) && w.Pct < bars.ModelThreshold {
				return true
			}
		}
	}
	return false
}

// fableBelow names the accounts that report a Fable window and sit under bar
// on every one of them.
func (v policyView) fableBelow(bar float64) map[string]bool {
	out := map[string]bool{}
	for num, u := range v.usage {
		found, under := false, true
		for _, w := range u.Scoped {
			if strings.Contains(strings.ToLower(w.Name), "fable") {
				found = true
				under = under && w.Pct < bar
			}
		}
		out[num] = found && under
	}
	return out
}

// scopedNamesFor is every per-model window name an account reports for one of
// the model families, sorted.
func scopedNamesFor(usage map[string]*oauth.Usage, families map[string]bool) []string {
	names := map[string]bool{}
	for _, u := range usage {
		for _, w := range u.Scoped {
			for family := range families {
				if strings.Contains(strings.ToLower(w.Name), family) {
					names[w.Name] = true
				}
			}
		}
	}
	models := make([]string, 0, len(names))
	for name := range names {
		models = append(models, name)
	}
	sort.Strings(models)
	return models
}
