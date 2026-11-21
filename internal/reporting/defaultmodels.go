package reporting

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/logging"
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
	// Shared-store status records expire after a day. An independent login
	// binds its session to that identity for future native resumes.
	defaultModelKeep = 24 * time.Hour
)

type defaultSessionModel struct {
	Family string  `json:"family"`
	SeenAt float64 `json:"seenAt"`
	// Store is "fable" for a session on the Fable store, "" for the default
	// store's (DESIGN A100).
	Store   string       `json:"store,omitempty"`
	Binding string       `json:"binding,omitempty"`
	Link    *SessionLink `json:"link,omitempty"`
	// Before is the saved record until the atomic link rename commits a move.
	Before    *defaultSessionModel `json:"beforeMove,omitempty"`
	bad       *invalidSessionRecord
	recovered bool
}

type invalidSessionRecord struct {
	raw json.RawMessage
	err error
}

func (r defaultSessionModel) MarshalJSON() ([]byte, error) {
	if r.bad != nil {
		return r.bad.raw, nil
	}
	type fields defaultSessionModel
	return json.Marshal(fields(r))
}

func (r *defaultSessionModel) UnmarshalJSON(raw []byte) error {
	type fields defaultSessionModel
	var v struct {
		fields
		Family *string  `json:"family"`
		SeenAt *float64 `json:"seenAt"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.Family == nil || v.SeenAt == nil {
		return errors.New("missing session family or observation time")
	}
	*r = defaultSessionModel(v.fields)
	r.Family, r.SeenAt = *v.Family, *v.SeenAt
	return nil
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

func (r defaultSessionModel) binding() string {
	if r.Binding != "" {
		return r.Binding
	}
	if strings.HasPrefix(r.Store, "mythos/") {
		return r.Store
	}
	return ""
}

func SessionBinding(root, sessionID string) (string, error) {
	sessions, err := loadDefaultSessionModels(root)
	if err != nil {
		return "", err
	}
	rec := sessions[sessionID]
	if rec.bad != nil {
		return "", rec.bad.err
	}
	return rec.binding(), nil
}

func readDefaultSessionModels(root string) map[string]defaultSessionModel {
	sessions, _ := loadDefaultSessionModels(root)
	for id, rec := range sessions {
		if rec.bad != nil {
			delete(sessions, id)
		}
	}
	return sessions
}

func loadDefaultSessionModels(root string) (map[string]defaultSessionModel, error) {
	raw, err := os.ReadFile(filepath.Join(root, defaultModelsFile))
	if os.IsNotExist(err) {
		return map[string]defaultSessionModel{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Sessions map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("could not read saved session identities: %w", err)
	}
	if file.Sessions == nil {
		return nil, fmt.Errorf("saved session identities are missing the sessions map")
	}
	records := make(map[string]defaultSessionModel, len(file.Sessions))
	for id, raw := range file.Sessions {
		var rec defaultSessionModel
		err := json.Unmarshal(raw, &rec)
		if err == nil {
			_, _, err = savedLink(root, id, rec)
		}
		if err == nil && rec.Before != nil {
			if _, _, err = savedLink(root, id, *rec.Before); err == nil {
				rec, err = committedLinkRecord(root, rec)
				rec.recovered = err == nil
			}
		}
		if err != nil {
			rec = defaultSessionModel{bad: &invalidSessionRecord{raw: raw, err: fmt.Errorf("unreadable session %q: %w", id, err)}}
			warnInvalidSession(root, id, rec.bad.err)
		}
		records[id] = rec
	}
	return records, nil
}

func warnInvalidSession(root, id string, why error) {
	dir := filepath.Join(root, ".session-record-warnings")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	// O_EXCL claims the warning across status lines, ticks and independent CLI processes.
	name := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	f.Close()
	logging.New(root, false).Warning(why.Error())
}

func expireSessionModels(records map[string]defaultSessionModel, now time.Time) bool {
	cutoff := float64(now.Add(-defaultModelKeep).UnixNano()) / 1e9
	changed := false
	for id, rec := range records {
		if rec.bad != nil || rec.SeenAt >= cutoff || rec.binding() != "" {
			continue
		}
		if link := rec.Link; link != nil && (link.ResumeMythos != "" || procdetect.RecordAlive(link.PID, link.ProcStart, "")) {
			continue
		}
		delete(records, id)
		changed = true
	}
	return changed
}

var modelFamilies = []string{"fable", "mythos", "opus", "sonnet", "haiku"}

// MythosModel names Claude Mythos in the policy's unservable list, the
// engine's events and the counting caption (DESIGN A106).
const MythosModel = oauth.MythosModel

// ModelFamily is the family of a Claude model id or alias ("fable", "mythos",
// "opus", "sonnet", "haiku"), "" for anything else. A provider's id, such as
// us.anthropic.claude-fable-5-2-v1:0, answers the one family it names as a
// whole word.
func ModelFamily(model string) string {
	if family := ClaudeFamily(model); family != "" {
		return family
	}
	m := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "[1m]")
	found := ""
	for _, word := range strings.FieldsFunc(m, func(ch rune) bool { return !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9') }) {
		if slices.Contains(modelFamilies, word) {
			if found != "" && found != word {
				return ""
			}
			found = word
		}
	}
	return found
}

// ClaudeFamily is the family of a Claude model alias or claude-<family>-… id,
// "" for anything else, a provider's id included.
func ClaudeFamily(model string) string {
	m := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "[1m]")
	for _, ch := range m {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
			return ""
		}
	}
	for _, family := range modelFamilies {
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
// store (A77). An unknown model, a provider's id (such a session runs on no
// login of tycswap's; v0.15.1 recorded none) or a busy lock records nothing.
func RecordDefaultSessionModel(root, sessionID, model, store string, now time.Time) (bool, error) {
	return recordSessionModel(root, sessionID, model, store, now, 50*time.Millisecond)
}

func RecordSessionStart(root, sessionID, model, store string, now time.Time) error {
	_, err := recordSessionModel(root, sessionID, model, store, now, filelock.DefaultTimeout)
	return err
}

func recordSessionModel(root, sessionID, model, store string, now time.Time, timeout time.Duration) (bool, error) {
	family := ClaudeFamily(model)
	if sessionID == "" || family == "" {
		return false, nil
	}
	lock := filelock.New(filepath.Join(root, ".default_models.lock"), timeout)
	ok, err := lock.Acquire(0)
	if err != nil {
		return false, err
	}
	if !ok {
		if timeout == filelock.DefaultTimeout {
			return false, fmt.Errorf("timed out recording session identity")
		}
		return false, nil
	}
	defer lock.Release()
	sessions, err := loadDefaultSessionModels(root)
	if err != nil {
		return false, err
	}
	at := float64(now.UnixNano()) / 1e9
	old, seen := sessions[sessionID]
	if old.bad != nil {
		return false, old.bad.err
	}
	if old.Link != nil {
		link, _, err := savedLink(root, sessionID, old)
		if err != nil {
			return false, err
		}
		if procdetect.RecordAlive(link.PID, link.ProcStart, "") {
			return false, nil
		}
		// A direct resume took the fail-open path after this linked process ended.
	}
	binding := old.binding()
	if strings.HasPrefix(store, "fable/") {
		if binding != "" && binding != store {
			return false, fmt.Errorf("the session already belongs to another native login")
		}
		binding, store = store, "fable"
	}
	changed := !seen || old.Link != nil || old.Family != family || old.Store != store || binding != old.binding()
	if !changed && at-old.SeenAt < defaultModelRefresh.Seconds() && at >= old.SeenAt {
		return false, nil
	}
	next := defaultSessionModel{Family: family, SeenAt: at, Store: store, Binding: binding}
	if next.Binding == next.Store {
		next.Binding = ""
	}
	sessions[sessionID] = next
	expireSessionModels(sessions, now)
	return changed, saveSessionModels(root, sessions)
}

func saveSessionModels(root string, sessions map[string]defaultSessionModel) error {
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "sessions": sessions})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(root, defaultModelsFile), raw, atomicfile.Opts{})
}

// PolicyResult is the default-session policy for one tick (DESIGN A76, A77,
// A91): the settings to decide with, the accounts left to Fable work (nil
// reserves none), the derived models dropped because no usable account can
// serve them, and the model families the live default sessions run.
type PolicyResult struct {
	Settings       settings.AutoSwitchSettings
	CountedWindows []string // Reported names for display. Settings.Model selects the meter per account.
	Reserved       func(num string) bool
	Unservable     []string
	Sessions       []string
	// Accounts serving a dedicated login are last in the default rotation.
	ReservedLast func(num string) bool
	// Serves is the accounts the default login may run on while a live
	// session runs Mythos and Mythos can be served, the accounts marked for
	// it; nil serves every account. Mythos is the marked accounts, in
	// sequence order, while such a session runs (DESIGN A106).
	Serves func(num string) bool
	Mythos []string
	// FableOnDefault counts the live Fable sessions on the default login the
	// policy does not follow while the Fable store is usable (A108).
	FableOnDefault      int
	OpusOnly            bool
	IndependentSessions map[int]string
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
// nothing. A live Mythos session restricts the login to the accounts marked
// for Mythos, while one is in the pool (A106).
//
// While the Fable store is usable (A102) it follows neither the Fable
// store's sessions nor a Fable session on the default login (A108), and
// always reserves the Fable pool's accounts with Fable room and the Fable
// store's live account, the latter last.
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
		live := fable.Live.Slot
		r.Reserved = fableReserved(s, live, viewOf().fableBelow(base.ModelThreshold))
		r.ReservedLast = func(num string) bool { return num != "" && num == live }
	}
	records := readDefaultSessionModels(s.BackupDir())
	if len(records) == 0 {
		return r
	}
	sessions, ides, err := procdetect.GetRunningInstancesErr(s.DefaultProfileDir())
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
		if strings.HasPrefix(rec.Store, "mythos/") || fable.Usable && rec.Store == "fable" {
			if r.IndependentSessions == nil {
				r.IndependentSessions = map[int]string{}
			}
			r.IndependentSessions[session.PID] = rec.Store
			continue
		}
		if fable.Usable && rec.Family == "fable" {
			r.FableOnDefault++
			continue
		}
		if rec.Link != nil && !rec.Link.SharedMythos && rec.Store == "" && rec.Family == "mythos" {
			// Moving the shared login would also move unrelated sessions.
			continue
		}
		families[rec.Family] = true
	}
	occupied := map[string]bool{}
	for _, scope := range r.IndependentSessions {
		if strings.HasPrefix(scope, "mythos/") {
			occupied[scope] = true
		}
	}
	if len(occupied) > 0 {
		data, err := s.SequenceForUpdate()
		if err == nil {
			active := map[string]bool{}
			for _, n := range data.Sequence {
				num := strconv.Itoa(n)
				_, email, org, err := s.ResolveAccountFrom(data, num)
				if err == nil && occupied["mythos/"+filepath.Base(store.MythosLoginDir(s.BackupDir(), email, org))] {
					active[num] = true
				}
			}
			before := r.ReservedLast
			r.ReservedLast = func(num string) bool { return active[num] || before != nil && before(num) }
			reserved := r.Reserved
			r.Reserved = func(num string) bool { return active[num] || reserved != nil && reserved(num) }
		}
	}
	r.OpusOnly = allKnown && len(ides) == 0 && r.FableOnDefault == 0 && len(families) == 1 && families["opus"]
	if len(families) == 0 {
		return r
	}
	for family := range families {
		r.Sessions = append(r.Sessions, family)
	}
	sort.Strings(r.Sessions)
	view := viewOf()
	followMythos := families["mythos"]
	delete(families, "mythos")
	for _, name := range scopedNamesFor(view.usage, families) {
		if view.serves(name, base) {
			r.CountedWindows = append(r.CountedWindows, name)
		} else {
			r.Unservable = append(r.Unservable, name)
		}
	}
	counted := r.CountedWindows
	if followMythos {
		data, _ := s.ReadSequence()
		r.Mythos = markedForMythos(data)
		var meters []string
		for _, num := range r.Mythos {
			if view.pool[num] {
				meters = append(meters, oauth.FamilyWindows(view.serving[num], "mythos")...)
			}
		}
		withMythos := counted
		if len(meters) > 0 {
			withMythos = append(slices.Clone(counted), MythosModel)
		}
		if view.mythosServed(data, base, withMythos) {
			r.Serves = s.MythosCapable
			counted = withMythos
			r.CountedWindows = append(r.CountedWindows, meters...)
			sort.Strings(r.CountedWindows)
		} else {
			r.Unservable = append(r.Unservable, MythosModel)
		}
	}
	if model := strings.Join(counted, ","); model != "" {
		r.Settings.Model = &model
		display := strings.Join(r.CountedWindows, ",")
		r.CountedWindows = settings.ParseModelNames(&display)
	}
	if fable.Usable || !allKnown || families["fable"] {
		return r
	}
	room := view.fableBelow(base.ModelThreshold)
	reserved := r.Reserved
	r.Reserved = func(num string) bool { return reserved != nil && reserved(num) || room[num] && s.FableCapable(num) }
	return r
}

// markedForMythos lists the accounts marked for Mythos, in sequence order.
func markedForMythos(data *store.SequenceData) []string {
	var out []string
	if data != nil {
		for _, n := range data.Sequence {
			if num := strconv.Itoa(n); store.MythosFrom(data, num) {
				out = append(out, num)
			}
		}
	}
	return out
}

// mythosServed reports whether Mythos can be served, A91's rule for a model:
// a marked account of the pool sits below the bars the engine moves at on its
// 5h, 7d and counted windows, or the live account is marked and not at a hard
// limit. Otherwise a Mythos session restricts nothing, so marks without room,
// or none, never freeze rotation (DESIGN A106).
func (v policyView) mythosServed(data *store.SequenceData, bars settings.AutoSwitchSettings, counted []string) bool {
	for num, usable := range v.pool {
		u := v.serving[num]
		if !usable || u == nil || !store.MythosFrom(data, num) {
			continue
		}
		if num == v.live {
			if h := oauth.AccountHeadroom(u, counted); h != nil && *h > 0 {
				return true
			}
		}
		if belowBars(u, bars, counted) {
			return true
		}
	}
	return false
}

// belowBars reports whether u sits below the 5h, 7d and model bars on its
// windows and on every counted one; an absent 5h window is room, an absent
// 7d or counted window is not.
func belowBars(u *oauth.Usage, bars settings.AutoSwitchSettings, counted []string) bool {
	if u.SevenDay == nil || u.SevenDay.Pct >= bars.SevenDayThreshold || u.FiveHour != nil && u.FiveHour.Pct >= bars.FiveHourThreshold {
		return false
	}
	for _, name := range oauth.ResolveModelWindows(u, counted) {
		found := false
		for _, w := range u.Scoped {
			if strings.EqualFold(w.Name, name) {
				found = true
				if w.Pct >= bars.ModelThreshold {
					return false
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
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
// Fable login, and not quarantined in the Fable state file (FableQuarantine).
// s is the default store.
func FablePool(s *store.Store) []string {
	_, err := s.ForFable()
	data, _ := s.ReadSequence()
	if err != nil || data == nil {
		return nil
	}
	held := FableQuarantine(s)
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
	live    string
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
		if info.IsActive {
			v.live = num
		}
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
