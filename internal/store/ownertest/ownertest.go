// Package ownertest is test support: the credential-ownership fixture that the
// read-count, freshness and equality tests share, and a counter of the
// identity files read and the profiles whose live PIDs are scanned.
package ownertest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/sessprofile"
	"github.com/tyclab/tycswap/internal/store"
)

// Options shapes the fixture. Accounts past 6 are free.
type Options struct {
	Accounts int
	// PadKiB grows the default login's and the three profiles' .claude.json
	// with project entries, as a long-used Claude Code config is.
	PadKiB int
	// Journal adds a Fable group recovery journal naming account 6.
	Journal bool
	// Disagree adds a claim on the last account that names another identity.
	Disagree bool
	// Fable gives accounts 6 and 7 a Fable login, puts 6's in the Fable
	// store and marks every account able to continue Fable, where the
	// platform has a Fable store.
	Fable bool
}

// Fixture is what Build wrote. Account 1 is the live default login with a
// default session running; 2 and 3 are held by former session profiles that
// run a session; 4's former profile is idle and signed out; 5 is held by the
// Opus group; the rest are free. With Options.Fable the roster has at least
// 7 accounts.
type Fixture struct {
	Store  *store.Store
	Emails map[string]string
	Orgs   map[string]string
	// Legacy is each former session profile's directory by account.
	Legacy map[string]string
	Group  string
}

const farFuture = 4102444800000

// Creds is account num's credential as the fixture stores it.
func Creds(num string) string {
	return `{"claudeAiOauth":{"accessToken":"at-` + num + `","refreshToken":"rt-` + num + `","expiresAt":` + strconv.Itoa(farFuture) + `,"scopes":["user:inference"]}}`
}

// Identity is a .claude.json naming email and org, grown by padKiB.
func Identity(email, org string, padKiB int) string {
	var b strings.Builder
	b.WriteString(`{"numStartups":412,"theme":"dark","oauthAccount":{"accountUuid":"u-` + email + `","emailAddress":"` + email +
		`","organizationUuid":"` + org + `","organizationName":"","planDisplayName":"Max"},"projects":{`)
	for i := 0; b.Len() < padKiB*1024; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"/home/user/work/project-%d":{"allowedTools":[],"history":[{"display":"run the tests in package %d","pastedContents":{}}],`+
			`"mcpServers":{},"lastCost":%d.25,"lastDuration":%d,"lastTotalInputTokens":%d,"lastTotalOutputTokens":%d,`+
			`"hasTrustDialogAccepted":true,"exampleFiles":["main.go","store.go"],"lastSessionId":"%08d-0000-4000-8000-000000000000"}`,
			i, i, i%7, 1000+i, 20000+i, 3000+i, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

// Build writes the fixture into s's isolated home and store.
func Build(t testing.TB, s *store.Store, opts Options) *Fixture {
	t.Helper()
	if opts.Accounts < 6 {
		opts.Accounts = 6
	}
	if opts.Fable && opts.Accounts < 7 {
		opts.Accounts = 7
	}
	root := s.BackupDir()
	f := &Fixture{Store: s, Emails: map[string]string{}, Orgs: map[string]string{}, Legacy: map[string]string{}, Group: groups.ProfileDir(root, groups.Opus)}
	accounts := map[string]any{}
	var sequence []int
	for n := 1; n <= opts.Accounts; n++ {
		num := strconv.Itoa(n)
		f.Emails[num] = string(rune('a'+n-1)) + "@example.com"
		if n == 5 {
			f.Orgs[num] = "org-5"
		}
		rec := map[string]any{"email": f.Emails[num], "uuid": "", "organizationUuid": f.Orgs[num], "organizationName": "",
			"added": "2026-01-01T00:00:00Z", "plan": "Max"}
		if opts.Fable {
			rec["fableContinue"] = true
		}
		accounts[num] = rec
		sequence = append(sequence, n)
	}
	active := 1
	roster, err := json.Marshal(map[string]any{"activeAccountNumber": active, "lastUpdated": "2026-11-25T08:00:00Z", "sequence": sequence, "accounts": accounts})
	must(t, err)
	must(t, os.MkdirAll(root, 0o700))
	write(t, s.SequenceFile, string(roster))
	for n := 1; n <= opts.Accounts; n++ {
		num := strconv.Itoa(n)
		must(t, s.Creds.WriteBackup(num, f.Emails[num], Creds(num)))
		must(t, s.WriteAccountConfig(num, f.Emails[num], Identity(f.Emails[num], f.Orgs[num], 0)))
	}

	write(t, s.DefaultConfigPath(), Identity(f.Emails["1"], "", opts.PadKiB))
	must(t, s.Creds.WriteActive(Creds("1")))
	LiveSession(t, s.DefaultProfileDir())

	claims := map[string]groups.Owner{}
	for _, num := range []string{"2", "3", "4"} {
		dir := s.SessionDir(num, f.Emails[num])
		f.Legacy[num] = dir
		write(t, filepath.Join(dir, ".claude.json"), Identity(f.Emails[num], f.Orgs[num], opts.PadKiB))
		write(t, filepath.Join(dir, "history.jsonl"), "kept history\n")
		if num == "4" {
			continue
		}
		write(t, filepath.Join(dir, ccfile.CredentialsFileName), Creds(num))
		LiveSession(t, dir)
		claims[num] = groups.Owner{Scope: "legacy:" + num, ProfileDir: dir, Account: groups.Account{Number: num, Email: f.Emails[num], OrgUUID: f.Orgs[num]}}
	}

	write(t, filepath.Join(f.Group, ".claude.json"), Identity(f.Emails["5"], f.Orgs["5"], 0))
	write(t, filepath.Join(f.Group, ccfile.CredentialsFileName), Creds("5"))
	five := groups.Account{Number: "5", Email: f.Emails["5"], OrgUUID: f.Orgs["5"]}
	must(t, groups.WriteJSON(groups.ActivePath(root, groups.Opus), five))
	claims["5"] = groups.Owner{Scope: string(groups.Opus), ProfileDir: f.Group, Account: five}

	if opts.Journal {
		target := groups.Account{Number: "6", Email: f.Emails["6"], OrgUUID: f.Orgs["6"]}
		must(t, os.MkdirAll(groups.ProfileDir(root, groups.Fable), 0o700))
		must(t, groups.WriteJSON(groups.JournalPath(root, groups.Fable), groups.Journal{Version: 1, Group: groups.Fable, Target: target, TargetFingerprint: "fixture"}))
	}
	if opts.Disagree {
		num := strconv.Itoa(opts.Accounts)
		other := "other@example.com"
		claims[num] = groups.Owner{Scope: "legacy:" + num, ProfileDir: sessprofile.SessionDirFor(root, num, other), Account: groups.Account{Number: num, Email: other}}
	}
	must(t, (&groups.Registry{Version: 1, Claims: claims}).Save(root))
	if fable, err := s.ForFable(); opts.Fable && err == nil {
		must(t, s.SetupFableDirectories())
		for _, num := range []string{"6", "7"} {
			must(t, fable.Creds.WriteBackup(num, f.Emails[num], Creds("fable-"+num)))
		}
		must(t, fable.Creds.WriteActive(Creds("fable-6")))
	}

	now := float64(time.Now().Unix())
	rows := map[string]any{}
	reset := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	for n := 1; n <= opts.Accounts; n++ {
		num := strconv.Itoa(n)
		rows[num] = map[string]any{"email": f.Emails[num], "organizationUuid": f.Orgs[num], "fetchedAt": now - 30,
			"nextPollAt": now + 3600, "pollIntervalS": 3600.0, "lastGood": map[string]any{
				"five_hour": map[string]any{"pct": 10.0, "resets_at": reset},
				"seven_day": map[string]any{"pct": 20.0, "resets_at": reset},
			}}
	}
	usage, err := json.Marshal(map[string]any{"schemaVersion": 2, "accounts": rows})
	must(t, err)
	write(t, filepath.Join(root, "cache", "usage.json"), string(usage))
	return f
}

// LiveSession records a Claude session of this test process in the profile at dir.
func LiveSession(t testing.TB, dir string) {
	t.Helper()
	pid := strconv.Itoa(os.Getpid())
	write(t, filepath.Join(dir, "sessions", pid+".json"), `{"pid":`+pid+`,"sessionId":"s-`+pid+`"}`)
}

// EndSessions removes every session record of the profile at dir.
func EndSessions(t testing.TB, dir string) {
	t.Helper()
	must(t, os.RemoveAll(filepath.Join(dir, "sessions")))
}

// Counts is how often each identity file was read and each profile's live
// PIDs were scanned since CountReads or Reset.
type Counts struct {
	mu       sync.Mutex
	identity map[string]int
	pids     map[string]int
}

// CountReads counts through the ccfile and store seams until the test ends.
func CountReads(t testing.TB) *Counts {
	t.Helper()
	c := &Counts{}
	c.Reset()
	identity, pids := ccfile.ObserveIdentityRead, store.ObservePIDScan
	ccfile.ObserveIdentityRead = func(path string) { c.add(c.identity, path) }
	store.ObservePIDScan = func(dir string) { c.add(c.pids, dir) }
	t.Cleanup(func() { ccfile.ObserveIdentityRead, store.ObservePIDScan = identity, pids })
	return c
}

func (c *Counts) add(m map[string]int, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m[filepath.Clean(key)]++
}

// Reset forgets what was counted.
func (c *Counts) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.identity, c.pids = map[string]int{}, map[string]int{}
}

// Most is the highest count of any one identity file and of any one profile.
func (c *Counts) Most() (identity, pids int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return most(c.identity), most(c.pids)
}

func most(m map[string]int) int {
	n := 0
	for _, v := range m {
		n = max(n, v)
	}
	return n
}

// String lists every count, for a failure message.
func (c *Counts) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lines []string
	for kind, m := range map[string]map[string]int{"identity": c.identity, "pids": c.pids} {
		total := 0
		for path, n := range m {
			lines = append(lines, fmt.Sprintf("  %s %4d %s", kind, n, path))
			total += n
		}
		lines = append(lines, fmt.Sprintf("  %s total %d over %d paths", kind, total, len(m)))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// AtMostOnce fails the test when op read an identity file or scanned a
// profile's live PIDs more than once, or read no identity file at all.
func (c *Counts) AtMostOnce(t testing.TB, op string) {
	t.Helper()
	identity, pids := c.Most()
	if identity == 0 {
		t.Fatalf("%s read no ownership input; the seams count nothing:\n%s", op, c)
	}
	if identity > 1 || pids > 1 {
		t.Fatalf("%s read an identity file up to %d times and scanned a profile's PIDs up to %d times, want once each:\n%s", op, identity, pids, c)
	}
}

func write(t testing.TB, path, body string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	must(t, os.WriteFile(path, []byte(body), 0o600))
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
