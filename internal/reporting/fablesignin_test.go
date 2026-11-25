// Tests for the accounts that need a Fable sign-in (DESIGN A115).
package reporting

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/store"
)

// signInRoster is accounts 1 to 7 on plans that run Fable, with Fable logins
// for logins: 4 is disabled, 5 an API key, 6 on a Pro plan and 7 allocated to
// Opus only.
func signInRoster(t *testing.T, logins ...string) *store.Store {
	t.Helper()
	s := newStore(t, nil, nil)
	records := map[string]any{}
	var sequence []int
	for n := 1; n <= 7; n++ {
		num := strconv.Itoa(n)
		email := "acct" + num + "@example.com"
		rec := map[string]any{"email": email, "organizationUuid": "", "organizationName": "", "subscriptionType": "ultra"}
		switch num {
		case "4":
			rec["disabled"] = true
		case "5":
			rec["kind"] = "api_key"
		case "6":
			rec["subscriptionType"] = "pro"
		case "7":
			rec[store.ModelsKey] = []string{"opus"}
		}
		records[num] = rec
		sequence = append(sequence, n)
		writeBackup(t, s, num, email, oauthCreds("at"+num, "rt"+num, 0), `{"oauthAccount":{"emailAddress":"`+email+`"}}`)
	}
	seq, err := json.Marshal(map[string]any{"activeAccountNumber": 1, "lastUpdated": "x", "sequence": sequence, "accounts": records})
	if err != nil {
		t.Fatal(err)
	}
	writeSequenceRaw(t, s, string(seq))
	s.Platform = platform.Linux
	f, err := s.ForFable()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetupFableDirectories(); err != nil {
		t.Fatal(err)
	}
	for _, num := range logins {
		if err := f.WriteAccountCredentials(num, "acct"+num+"@example.com", oauthCreds("fat"+num, "frt"+num, 0)); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// The set is the enabled, rotation-eligible accounts that run Fable and are
// not allocated away from it, without a usable Fable login: 2 has none, 3's
// reads "sign in again". 1's is usable; 4, 5, 6 and 7 are left out. A box
// without a Fable login has no set.
func TestAccountsThatNeedAFableSignIn(t *testing.T) {
	s := signInRoster(t, "1", "3")
	quarantineFable(t, s, "3", oauthCreds("fat3", "frt3", 0))
	if got := FableSignInNeeded(s); !reflect.DeepEqual(got, []string{"2", "3"}) {
		t.Errorf("FableSignInNeeded = %q, want 2 and 3", got)
	}
	if got := FableSignInNeeded(signInRoster(t)); len(got) != 0 {
		t.Errorf("without a Fable login: FableSignInNeeded = %q, want none", got)
	}
}

// The sentence names the accounts and the walk that signs them in.
func TestFableSignInHintNamesTheWalk(t *testing.T) {
	for _, tc := range []struct {
		nums []string
		want string
	}{
		{nil, ""},
		{[]string{"7"}, "Account-7 needs a Fable sign-in: tycswap fable --login"},
		{[]string{"7", "11"}, "Accounts 7 and 11 need a Fable sign-in: tycswap fable --login"},
		{[]string{"7", "11", "12"}, "Accounts 7, 11 and 12 need a Fable sign-in: tycswap fable --login"},
	} {
		if got := FableSignInHint(tc.nums); got != tc.want {
			t.Errorf("FableSignInHint(%q) = %q, want %q", tc.nums, got, tc.want)
		}
	}
}

// The fable object the dashboard's state takes carries the set, also while
// the Fable store is off; with every login usable it carries none.
func TestSnapshotCarriesTheFableSignInSet(t *testing.T) {
	s := signInRoster(t, "1", "3")
	quarantineFable(t, s, "3", oauthCreds("fat3", "frt3", 0))
	snap := Snapshot(s, map[string]bool{})
	if snap.Fable == nil || snap.Fable["state"] != "off" {
		t.Fatalf("snapshot fable = %s", jsonString(snap.Fable))
	}
	if got := jsonString(snap.Fable["withoutLogin"]); got != "[2,3]" {
		t.Errorf("fable.withoutLogin = %s, want [2,3]", got)
	}
	snap = Snapshot(signInRoster(t, "1", "2", "3"), map[string]bool{})
	if got, has := snap.Fable["withoutLogin"]; has {
		t.Errorf("every login usable: fable.withoutLogin = %v", got)
	}
}
