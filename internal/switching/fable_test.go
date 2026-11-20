// Tests for SwitchFable and EmptyFable (DESIGN A100): a switch of the Fable
// store moves no login between stores and writes nothing of the default's.
package switching

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
)

var fableEmails = map[string]string{"1": "one@example.com", "2": "two@example.com", "3": "three@example.com", "4": "four@example.com", "5": "five@example.com"}

func planCreds(access, refresh, plan string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":4102444800000,"subscriptionType":"` + plan + `"}}`
}

const fableMCPKeys = `"mcpOAuth":{"srv":{"accessToken":"store-m"}}`

func withStoreMCP(creds string) string {
	return strings.TrimSuffix(creds, "}") + "," + fableMCPKeys + "}"
}

// fableSwitchStore: accounts 1, 2 (Max, Fable logins), 3 (Pro, a Fable
// login), 4 (API key) and 5 (Max, no Fable login); the default login is 1's.
func fableSwitchStore(t *testing.T) (*store.Store, *store.Store) {
	t.Helper()
	s := newTestStore(t, nil)
	s.Platform = platform.Linux
	records := map[string]json.RawMessage{}
	for num, email := range fableEmails {
		rec := map[string]any{"email": email, "uuid": "u-" + num, "organizationUuid": "org-" + num, "organizationName": "Org " + num, "added": "t"}
		if num == "4" {
			rec["kind"] = "api_key"
		}
		records[num] = record(rec)
	}
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2, 3, 4, 5}, records))
	for num, email := range fableEmails {
		creds := planCreds("at-d"+num, "rt-d"+num, "max")
		if num == "3" {
			creds = planCreds("at-d3", "rt-d3", "pro")
		}
		if num == "4" {
			creds = "sk-ant-api03-four"
		}
		seedBackup(t, s, num, email, creds, "org-"+num)
	}
	seedLive(t, s, "one@example.com", "org-1", planCreds("at-d1", "rt-d1", "max"))
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	for num, plan := range map[string]string{"1": "max", "2": "max", "3": "pro"} {
		if err := f.WriteAccountCredentials(num, fableEmails[num], planCreds("at-f"+num, "rt-f"+num, plan)); err != nil {
			t.Fatal(err)
		}
	}
	return s, f
}

func fableStorePath(s *store.Store) string {
	return filepath.Join(paths.FableStoreDir(s.BackupDir()), ".credentials.json")
}

func setFableLive(t *testing.T, s *store.Store, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fableStorePath(s)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fableStorePath(s), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fableDefaultFiles is every file a Fable switch must leave byte-identical.
func fableDefaultFiles(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(p string) {
		raw, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		out[p] = string(raw)
	}
	add(filepath.Join(s.Home, ".claude", ".credentials.json"))
	add(filepath.Join(s.Home, ".claude.json"))
	add(s.SequenceFile)
	for _, dir := range []string{s.CredentialsDir, s.ConfigsDir} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			add(filepath.Join(dir, e.Name()))
		}
		out[dir+"/#count"] = string(rune('0' + len(entries)))
	}
	return out
}

func sameFiles(t *testing.T, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed", k)
		}
	}
	if len(after) != len(before) {
		t.Errorf("file count %d, want %d", len(after), len(before))
	}
}

func refOf(t *testing.T, v any) (int, string) {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return 0, ""
	}
	n, _ := m["number"].(*int)
	email, _ := m["email"].(string)
	if n == nil {
		return 0, email
	}
	return *n, email
}

func TestSwitchFableAToB(t *testing.T) {
	s, f := fableSwitchStore(t)
	rotated := planCreds("at-f1-new", "rt-f1-new", "max")
	setFableLive(t, s, withStoreMCP(rotated))
	if err := s.RecordFableLive("1", rotated); err != nil {
		t.Fatal(err)
	}
	before := fableDefaultFiles(t, s)
	locks := []string{filepath.Join(s.BackupDir(), "fable", "store.lock"), filepath.Join(paths.FableStoreDir(s.BackupDir()), ".storage-write.lock")}
	hooked := false
	fableBeforeInstall = func() {
		hooked = true
		if !f.Lock.Held() {
			t.Error("the store lock is not held during the install")
		}
		for _, l := range locks {
			if info, err := os.Stat(l); err != nil || !info.IsDir() {
				t.Errorf("%s not held during the install: %v", l, err)
			}
		}
	}
	t.Cleanup(func() { fableBeforeInstall = nil })

	res, err := SwitchFable(f, "2", FableSwitchOptions{Actor: "hand"})
	if err != nil {
		t.Fatal(err)
	}
	if !hooked {
		t.Fatal("the install seam never ran")
	}
	if res["switched"] != true || res["reason"] != "switched" || res["store"] != "fable" {
		t.Fatalf("result %v", res)
	}
	if n, e := refOf(t, res["from"]); n != 1 || e != "one@example.com" {
		t.Fatalf("from %v", res["from"])
	}
	if n, e := refOf(t, res["to"]); n != 2 || e != "two@example.com" {
		t.Fatalf("to %v", res["to"])
	}
	raw, _ := os.ReadFile(fableStorePath(s))
	if !sameJSON(string(raw), withStoreMCP(planCreds("at-f2", "rt-f2", "max"))) {
		t.Fatalf("store = %s, want 2's login with the store's mcpOAuth", raw)
	}
	if info, _ := os.Stat(fableStorePath(s)); !platform.IsWindows() && info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode %o", info.Mode().Perm())
	}
	got, _ := f.ReadAccountCredentials("1", fableEmails["1"])
	if fp := oauth.CredentialFingerprint(got); fp == nil || *fp != *oauth.CredentialFingerprint(rotated) || strings.Contains(got, "mcpOAuth") {
		t.Fatalf("1's Fable login = %s, want the rotated login's account part", got)
	}
	if prev, _ := f.Creds.ReadPrev("1", fableEmails["1"]); prev != planCreds("at-f1", "rt-f1", "max") {
		t.Fatalf("1's .prev = %q", prev)
	}
	sameFiles(t, before, fableDefaultFiles(t, s))
	for _, l := range locks {
		if _, err := os.Stat(l); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind", l)
		}
	}
	if f.Lock.Held() {
		t.Error("the store lock is still held")
	}
	if ls := s.ResolveFableLiveSlot(mustSeq(t, s)); ls.Slot != "2" || ls.Source != store.SourceBackup {
		t.Fatalf("live Fable slot %+v", ls)
	}
	var chain map[string]any
	if raw, err := os.ReadFile(filepath.Join(s.BackupDir(), "fable", "live_lineage.json")); err != nil || json.Unmarshal(raw, &chain) != nil || chain["slot"] != "2" {
		t.Fatalf("Fable chain record %v (%v)", chain, err)
	}
	if log, _ := os.ReadFile(filepath.Join(s.BackupDir(), "tycswap.log")); !strings.Contains(string(log), "Switched the Fable store to account 2 by hand") {
		t.Fatalf("log lacks the switch line:\n%s", log)
	}
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

func mustSeq(t *testing.T, s *store.Store) *store.SequenceData {
	t.Helper()
	data, err := s.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSwitchFableStashesAnUnknownLogin(t *testing.T) {
	s, f := fableSwitchStore(t)
	stranger := planCreds("at-x", "rt-x", "max")
	setFableLive(t, s, stranger)
	before := fableDefaultFiles(t, s)
	res, err := SwitchFable(f, "2", FableSwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res["switched"] != true || res["from"] != nil && res["from"].(map[string]any) != nil {
		t.Fatalf("result %v", res)
	}
	rows, _ := f.Creds.ListUnclaimed()
	if len(rows) != 1 {
		t.Fatalf("unclaimed %v", rows)
	}
	for _, row := range rows {
		if row["lineage"] != "fable" {
			t.Fatalf("unclaimed row %v, want lineage fable", row)
		}
	}
	if _, err := os.Stat(filepath.Join(paths.FableCredentialsDir(s.BackupDir()), ".unclaimed-manifest.json")); err != nil {
		t.Fatalf("stash not in credentials-fable/: %v", err)
	}
	sameFiles(t, before, fableDefaultFiles(t, s))
}

func TestSwitchFableAlreadyActive(t *testing.T) {
	s, f := fableSwitchStore(t)
	live := withStoreMCP(planCreds("at-f2", "rt-f2", "max"))
	setFableLive(t, s, live)
	res, err := SwitchFable(f, "two@example.com", FableSwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res["switched"] != false || res["reason"] != "already-active" || res["store"] != "fable" {
		t.Fatalf("result %v", res)
	}
	if raw, _ := os.ReadFile(fableStorePath(s)); string(raw) != live {
		t.Fatal("already-active rewrote the store")
	}
}

func TestSwitchFableRefusals(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"9", "Account-9 does not exist"},
		{"4", "Account-4 is an API-key account"},
		{"5", "Account-5 has no Fable login: tycswap add --login --lineage fable --slot 5"},
		{"3", "Account-3's plan offers no Fable"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			s, f := fableSwitchStore(t)
			setFableLive(t, s, withStoreMCP(planCreds("at-f1", "rt-f1", "max")))
			before := snapshotTree(t, s.BackupDir())
			_, err := SwitchFable(f, tc.id, FableSwitchOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
			after := snapshotTree(t, s.BackupDir())
			for k, v := range before {
				if after[k] != v {
					t.Errorf("%s changed", k)
				}
			}
			if len(after) != len(before) {
				t.Errorf("files %d, want %d", len(after), len(before))
			}
		})
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			out[p+"/"] = ""
			return nil
		}
		if filepath.Base(p) == "tycswap.log" {
			return nil
		}
		raw, _ := os.ReadFile(p)
		out[p] = string(raw)
		return nil
	})
	return out
}

func TestEmptyFable(t *testing.T) {
	for _, tc := range []struct {
		name, live, want string
	}{
		{"with seat-wide keys", withStoreMCP(planCreds("at-f1", "rt-f1", "max")), "{" + fableMCPKeys + "}"},
		{"without", planCreds("at-f1", "rt-f1", "max"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := fableSwitchStore(t)
			setFableLive(t, s, tc.live)
			before := fableDefaultFiles(t, s)
			res, err := EmptyFable(f)
			if err != nil {
				t.Fatal(err)
			}
			if res["reason"] != "emptied" || res["switched"] != true || res["store"] != "fable" {
				t.Fatalf("result %v", res)
			}
			if n, _ := refOf(t, res["from"]); n != 1 {
				t.Fatalf("from %v", res["from"])
			}
			raw, err := os.ReadFile(fableStorePath(s))
			switch {
			case tc.want == "" && !errors.Is(err, os.ErrNotExist):
				t.Fatalf("store file %s (%v), want none", raw, err)
			case tc.want != "" && string(raw) != tc.want:
				t.Fatalf("store file %s, want %s", raw, tc.want)
			}
			if st := s.FableStore(); st.On {
				t.Fatalf("FableStore after --empty = %+v", st)
			}
			if got, _ := f.ReadAccountCredentials("1", fableEmails["1"]); !sameJSON(got, planCreds("at-f1", "rt-f1", "max")) {
				t.Fatalf("1's Fable login = %q", got)
			}
			sameFiles(t, before, fableDefaultFiles(t, s))
			again, err := EmptyFable(f)
			if err != nil || again["switched"] != false || again["reason"] != "emptied" {
				t.Fatalf("second EmptyFable = %v, %v", again, err)
			}
		})
	}
}

func TestFailedRecoveryWriteLeavesNoReaderAndCanRetry(t *testing.T) {
	s, f := fableSwitchStore(t)
	if err := f.SetFableAllocationParked(true); err != nil {
		t.Fatal(err)
	}
	path := fableStorePath(s)
	fableBeforeInstall = func() {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { fableBeforeInstall = nil })
	identity := strings.Repeat("a", 64)
	check := func(*store.Store, *store.SequenceData) error { return nil }
	if err := RecoverFable(f, "2", identity, check); err == nil {
		t.Fatal("expected failed credential write")
	}
	reader, err := store.ReadFableReaders(s.BackupDir())
	if err != nil || reader.Identity != "" {
		t.Fatalf("failed write reserved a reader: %+v, %v", reader, err)
	}
	if parked, err := store.FableAllocationParked(s.BackupDir()); err != nil || !parked {
		t.Fatalf("lost parked state: %v %v", parked, err)
	}
	fableBeforeInstall = nil
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := RecoverFable(f, "2", identity, check); err != nil {
		t.Fatal(err)
	}
	reader, err = store.ReadFableReaders(s.BackupDir())
	if err != nil || reader.Identity != identity || !reader.InUse() {
		t.Fatalf("completed write lacks reader: %+v, %v", reader, err)
	}
}
