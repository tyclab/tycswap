// Tests for the Fable store on list, status and the snapshot (DESIGN A104).
package reporting

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

// quarantineFable marks num's Fable login dead in the Fable state file, as
// the Fable engine does on invalid_grant.
func quarantineFable(t *testing.T, s *store.Store, num, creds string) {
	t.Helper()
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if err := autoswitch.Quarantine(f.StatePath(), s.Clk, num, "acct"+num+"@example.com", oauth.ErrInvalidGrant, creds, func(autoswitch.Event) {}); err != nil {
		t.Fatal(err)
	}
}

// roundTrip is v as a JSON reader sees it.
func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func listRows(t *testing.T, payload map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, r := range payload["accounts"].([]any) {
		row := r.(map[string]any)
		out[jsonString(row["number"])] = row
	}
	return out
}

func jsonString(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// list --json names the Fable store's account and each Fable login: 8 holds
// the store, 3's login is usable, 11's is dead; 4 and 7 have none.
func TestListNamesTheFableStoreAndItsLogins(t *testing.T) {
	s, _ := fableIncident(t, nil, nil, "8", "3", "8", "11")
	quarantineFable(t, s, "11", oauthCreds("fat11", "frt11", 0))
	got, err := ListAccounts(s, false, true, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	payload := roundTrip(t, got)
	if n := payload["fableAccountNumber"]; n != 8.0 {
		t.Fatalf("fableAccountNumber = %v, want 8", n)
	}
	rows := listRows(t, payload)
	want := map[string]string{"3": `{"live":false,"status":"ok"}`, "8": `{"live":true,"status":"ok"}`, "11": `{"live":false,"status":"sign in again"}`}
	for _, num := range incidentSlots {
		login, has := rows[num]["fableLogin"]
		if want[num] == "" {
			if has {
				t.Errorf("Account-%s has no Fable login, got fableLogin %v", num, login)
			}
			continue
		}
		if got := jsonString(login); got != want[num] {
			t.Errorf("Account-%s fableLogin = %s, want %s", num, got, want[num])
		}
	}

	var buf bytes.Buffer
	infos := BuildAccountsInfo(s)
	renderAccounts(&buf, s, infos, CollectUsageEntries(s, infos, map[string]bool{}), false)
	tags := map[string]string{"3": "(Fable login)", "8": "(Fable store)", "11": "(Fable login: sign in again)"}
	for _, line := range strings.Split(buf.String(), "\n") {
		for num, tag := range tags {
			if strings.HasPrefix(strings.TrimSpace(line), num+": ") {
				if !strings.HasSuffix(line, tag) {
					t.Errorf("Account-%s line %q does not end in %s", num, line, tag)
				}
				delete(tags, num)
			}
		}
		for _, num := range []string{"4", "7"} {
			if strings.HasPrefix(strings.TrimSpace(line), num+": ") && strings.Contains(line, "Fable") {
				t.Errorf("Account-%s has no Fable login: %q", num, line)
			}
		}
	}
	if len(tags) > 0 {
		t.Errorf("rows missing: %v\n%s", tags, buf.String())
	}
	if !strings.Contains(buf.String(), "3: acct3@example.com [personal] (active) at limit: 5h (Fable login)\n") {
		t.Errorf("the existing markers stay as they were, the Fable tag after them:\n%s", buf.String())
	}
}

// An emptied Fable store: no account number, every login not live.
func TestListWithTheFableStoreOff(t *testing.T) {
	s, _ := fableIncident(t, nil, nil, "", "3", "8")
	got, err := ListAccounts(s, false, true, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	payload := roundTrip(t, got)
	if n, has := payload["fableAccountNumber"]; !has || n != nil {
		t.Fatalf("fableAccountNumber = %v (present %v), want null", n, has)
	}
	if got := jsonString(listRows(t, payload)["8"]["fableLogin"]); got != `{"live":false,"status":"ok"}` {
		t.Fatalf("Account-8 fableLogin = %s", got)
	}
}

// Without a Fable login nothing is added, to JSON or text.
func TestListWithoutFableLogins(t *testing.T) {
	s, _ := fableIncident(t, nil, nil, "")
	got, err := ListAccounts(s, false, true, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if raw := jsonString(got); strings.Contains(raw, "fable") {
		t.Fatalf("list --json without Fable logins mentions fable: %s", raw)
	}
	var buf bytes.Buffer
	infos := BuildAccountsInfo(s)
	renderAccounts(&buf, s, infos, CollectUsageEntries(s, infos, map[string]bool{}), false)
	if strings.Contains(buf.String(), "(Fable") {
		t.Fatalf("the human list without Fable logins has a Fable tag:\n%s", buf.String())
	}
	payload, err := Status(s, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := payload.(map[string]any)["fable"]; has {
		t.Fatalf("status --json without Fable logins has a fable key")
	}
	buf.Reset()
	renderStatus(&buf, s)
	if strings.Contains(buf.String(), "Fable store") {
		t.Fatalf("status without Fable logins has a Fable store line:\n%s", buf.String())
	}
}

func statusFable(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	payload, err := Status(s, true)
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := roundTrip(t, payload)["fable"].(map[string]any)
	if !ok {
		t.Fatalf("status --json has no fable object: %s", jsonString(payload))
	}
	return obj
}

// status --json carries the fable object in each state; the paths
// flakelab's wake-parked reads (.fable.number, .fable.storeDir and
// .fable.usage.*) are pinned.
func TestStatusFableObject(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		s, _ := fableIncident(t, nil, nil, "8", "3", "8")
		obj := statusFable(t, s)
		storeDir := filepath.Clean(paths.FableStoreDir(s.BackupDir()))
		for key, want := range map[string]any{"state": "on", "storeDir": storeDir, "number": 8.0, "email": "acct8@example.com",
			"organizationName": "", "usageStatus": "ok", "source": store.SourceBackup} {
			if got := obj[key]; got != want {
				t.Errorf("fable.%s = %v, want %v", key, got, want)
			}
		}
		if _, has := obj["reason"]; has {
			t.Errorf("a usable store has no reason: %v", obj["reason"])
		}
		u, _ := obj["usage"].(map[string]any)
		seven, _ := u["sevenDay"].(map[string]any)
		scoped, _ := u["scoped"].([]any)
		if seven["pct"] != 93.0 || len(scoped) != 1 || scoped[0].(map[string]any)["pct"] != 100.0 {
			t.Errorf("fable.usage = %s, want Account-8's row (7d 93, Fable 100)", jsonString(u))
		}
		var buf bytes.Buffer
		renderStatus(&buf, s)
		if line := fableStoreLine(buf.String()); line != "Fable store: Account-8 (acct8@example.com) · Fable 100% · 7d 93%" {
			t.Errorf("human line = %q\n%s", line, buf.String())
		}
	})
	t.Run("unusable", func(t *testing.T) {
		s, _ := fableIncident(t, nil, nil, "8", "3", "8")
		quarantineFable(t, s, "8", oauthCreds("fat8", "frt8", 0))
		obj := statusFable(t, s)
		if obj["state"] != "unusable" || obj["number"] != 8.0 || obj["reason"] != "Account-8's Fable login: sign in again" {
			t.Errorf("fable = %s", jsonString(obj))
		}
		var buf bytes.Buffer
		renderStatus(&buf, s)
		if line := fableStoreLine(buf.String()); line != "Fable store: Account-8 (acct8@example.com) · Fable login: sign in again" {
			t.Errorf("human line = %q", line)
		}
	})
	t.Run("off", func(t *testing.T) {
		s, _ := fableIncident(t, nil, nil, "", "3", "8")
		obj := statusFable(t, s)
		storeDir := filepath.Clean(paths.FableStoreDir(s.BackupDir()))
		if obj["state"] != "off" || obj["storeDir"] != storeDir || obj["reason"] != "the store is empty" {
			t.Errorf("fable = %s", jsonString(obj))
		}
		if n, has := obj["number"]; !has || n != nil {
			t.Errorf("fable.number = %v (present %v), want null", n, has)
		}
		for _, key := range []string{"email", "usage", "usageStatus", "plan"} {
			if _, has := obj[key]; has {
				t.Errorf("an empty store has no %s", key)
			}
		}
		var buf bytes.Buffer
		renderStatus(&buf, s)
		if line := fableStoreLine(buf.String()); line != "Fable store: off" {
			t.Errorf("human line = %q", line)
		}
	})
}

// fableStoreLine is status's Fable store line; the line naming accounts with
// Fable room but no Fable login may follow it (DESIGN A112).
func fableStoreLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Fable store: ") {
			return line
		}
	}
	return ""
}

// The snapshot the dashboard serves carries the same fable object and each
// row's Fable login; without Fable logins it carries neither.
func TestSnapshotCarriesTheFableStore(t *testing.T) {
	s, _ := fableIncident(t, nil, nil, "8", "3", "8")
	snap := Snapshot(s, map[string]bool{})
	if snap.Fable == nil || snap.Fable["state"] != "on" || jsonString(snap.Fable["number"]) != "8" {
		t.Fatalf("snapshot fable = %s", jsonString(snap.Fable))
	}
	for _, a := range snap.Accounts {
		switch a.Number {
		case "3":
			if a.FableLogin == nil || a.FableLogin.Live || a.FableLogin.Status != FableLoginOK {
				t.Errorf("Account-3 FableLogin = %+v", a.FableLogin)
			}
		case "8":
			if a.FableLogin == nil || !a.FableLogin.Live {
				t.Errorf("Account-8 FableLogin = %+v", a.FableLogin)
			}
		default:
			if a.FableLogin != nil {
				t.Errorf("Account-%s has no Fable login, got %+v", a.Number, a.FableLogin)
			}
		}
	}

	none, _ := fableIncident(t, nil, nil, "")
	snap = Snapshot(none, map[string]bool{})
	if snap.Fable != nil {
		t.Fatalf("a snapshot without Fable logins has fable %v", snap.Fable)
	}
	for _, a := range snap.Accounts {
		if a.FableLogin != nil {
			t.Fatalf("Account-%s has FableLogin without Fable logins", a.Number)
		}
	}
}

// A Fable quarantine entry stands while its fingerprint names the account's
// stored Fable login or the login the Fable store holds; one whose login was
// signed in again since is no dead login, and one without a fingerprint
// stands (DESIGN A104).
func TestFableQuarantineLeavesOutLoginsSignedInAgain(t *testing.T) {
	s, _ := fableIncident(t, nil, nil, "8", "3", "4", "8", "11")
	putFableStore(t, s, oauthCreds("fat8b", "frt8b", 0))
	quarantineFable(t, s, "3", oauthCreds("fat3", "frt3", 0))
	quarantineFable(t, s, "4", oauthCreds("fat4-old", "frt4-old", 0))
	quarantineFable(t, s, "8", oauthCreds("fat8b", "frt8b", 0))
	f, _ := s.ForFable()
	raw, _ := os.ReadFile(f.StatePath())
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["quarantine"].(map[string]any)["11"] = map[string]any{"email": "acct11@example.com", "reason": "invalid_grant"}
	raw, _ = json.Marshal(state)
	if err := os.WriteFile(f.StatePath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	for num := range FableQuarantine(s) {
		got = append(got, num)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "11,3,8" {
		t.Errorf("Fable quarantine = %v, want 3 (its stored login), 8 (the store's login) and 11 (no fingerprint)", got)
	}
	status := map[string]string{}
	for _, a := range Snapshot(s, map[string]bool{}).Accounts {
		if a.FableLogin != nil {
			status[a.Number] = a.FableLogin.Status
		}
	}
	if want := map[string]string{"3": FableSignInAgain, "4": FableLoginOK, "8": FableSignInAgain, "11": FableSignInAgain}; !reflect.DeepEqual(status, want) {
		t.Errorf("fableLogin status = %v, want %v", status, want)
	}
	if pool := strings.Join(FablePool(s), ","); pool != "4" {
		t.Errorf("Fable pool = %s, want 4", pool)
	}
}
