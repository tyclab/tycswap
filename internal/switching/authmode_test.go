// Switching onto an API-key account changes how Claude Code authenticates, so
// the switch layer refuses it unless the caller recorded the user's approval
// for that exact slot (DESIGN A33).
package switching

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tyclab/tycswap/internal/store"
)

// TestApprovalIsSingleUseAndPerAccount: the approval a front-end records after
// asking the user covers exactly the switch it was given for — not the next
// one, and not a different account.
func TestApprovalIsSingleUseAndPerAccount(t *testing.T) {
	resetSeams(t)
	if takeApproval("5") {
		t.Fatal("no approval was given yet")
	}
	ApproveAPIKeySwitch("5")
	if takeApproval("9") {
		t.Error("an approval for #5 must not cover #9")
	}
	if !takeApproval("5") {
		t.Error("the approval for #5 should be honoured once")
	}
	if takeApproval("5") {
		t.Error("an approval must be consumed, not remembered")
	}
}

// TestRefusalNamesTheRestart: the refusal explains the part nobody expects,
// the running sessions.
func TestRefusalNamesTheRestart(t *testing.T) {
	msg := ErrAPIKeyNeedsApproval("7").Error()
	for _, want := range []string{"Account-7", "API key", "restart", "Confirm", "`tycswap switch 7`"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

// approvalSeat seeds slot 1 (a subscription login, live), slot 2 (an API-key
// account) and slot 3 (a second subscription login).
func approvalSeat(t *testing.T, s *store.Store) {
	t.Helper()
	ca := oauthCreds("acc-a", "ref-a")
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2, 3}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
		"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
		"3": record(map[string]any{"email": "c@x.com", "organizationUuid": ""}),
	}))
	seedBackup(t, s, "1", oauthSeatEmail, ca, "")
	seedBackup(t, s, "2", apiKeySeatEmail, apiKeySeatKey, "")
	seedBackup(t, s, "3", "c@x.com", oauthCreds("acc-c", "ref-c"), "")
	seedLive(t, s, oauthSeatEmail, "", ca)
}

// TestSwitchToAnAPIKeyNeedsApproval: without an approval the switch onto the
// API-key slot is refused before anything is written, with and without
// --force; with one it goes ahead, and the approval is used up by it.
func TestSwitchToAnAPIKeyNeedsApproval(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "switch"
		if force {
			name = "switch --force"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t, nil)
			approvalSeat(t, s)
			before := seatState(t, s)

			_, err := SwitchTo(s, "2", true, force)
			if err == nil {
				t.Fatalf("SwitchTo(2) without an approval succeeded; live credential now %q", readActiveCreds(t, s))
			}
			if err.Error() != ErrAPIKeyNeedsApproval("2").Error() {
				t.Errorf("error = %q, want the approval refusal", err)
			}
			assertSeatUnchanged(t, s, before)

			// An approval for another slot does not help.
			ApproveAPIKeySwitch("3")
			if _, err := SwitchTo(s, "2", true, force); err == nil {
				t.Fatal("an approval for #3 let a switch onto #2 through")
			}
			assertSeatUnchanged(t, s, before)

			ApproveAPIKeySwitch("2")
			if _, err := SwitchTo(s, "2", true, force); err != nil {
				t.Fatalf("SwitchTo(2) with an approval: %v", err)
			}
			if got := readActiveCreds(t, s); got != apiKeySeatKey {
				t.Errorf("live credential = %q, want the managed key", got)
			}

			// Back onto a subscription account needs nobody's approval.
			if _, err := SwitchTo(s, "1", true, false); err != nil {
				t.Fatalf("SwitchTo(1): %v", err)
			}
			// The approval was consumed by the switch it was given for.
			if _, err := SwitchTo(s, "2", true, false); err == nil {
				t.Error("a used approval let a second switch onto #2 through")
			}
		})
	}
}

// TestSwitchToAnAPIKeyByEmailNeedsApproval: the guard keys on the resolved
// slot, so naming the account by email or alias is no way around it.
func TestSwitchToAnAPIKeyByEmailNeedsApproval(t *testing.T) {
	s := newTestStore(t, nil)
	approvalSeat(t, s)
	if _, err := SwitchTo(s, apiKeySeatEmail, true, false); err == nil {
		t.Fatal("SwitchTo by email onto the API-key slot succeeded without an approval")
	}
	ApproveAPIKeySwitch("2")
	if _, err := SwitchTo(s, apiKeySeatEmail, true, false); err != nil {
		t.Fatalf("SwitchTo by email with the slot's approval: %v", err)
	}
}

// TestRotationNeverLandsOnAnAPIKey: the bare rotation (and --strategy) never
// changes the auth mode. It skips the API-key slot and says why, in both
// output modes, and the slot is no automatic-rotation target at all.
func TestRotationNeverLandsOnAnAPIKey(t *testing.T) {
	s := newTestStore(t, nil)
	approvalSeat(t, s)
	if got := s.SwitchableAccountNumbers(); !reflect.DeepEqual(got, []string{"1", "3"}) {
		t.Errorf("SwitchableAccountNumbers() = %v, want [1 3]", got)
	}
	if !s.AccountIsSwitchable("2") {
		t.Error("the API-key slot must stay a valid target for a switch by hand")
	}
	res, err := Switch(s, nil, true, nil, nil)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	m, _ := res.(map[string]any)
	if m["switched"] != true {
		t.Fatalf("Switch = %v, want a switch to #3", m)
	}
	to, _ := m["to"].(map[string]any)
	if n := to["number"]; n == nil || readActiveCreds(t, s) != oauthCreds("acc-c", "ref-c") {
		t.Errorf("switched to %v (live %q), want #3", n, readActiveCreds(t, s))
	}
	warnings, _ := json.Marshal(m["warnings"])
	if !strings.Contains(string(warnings), "Skipped Account-2 (API key") {
		t.Errorf("warnings = %s, want the API-key skip named", warnings)
	}
}

// TestRotationWithOnlyAnAPIKeyLeftSwitchesNowhere: with the API-key slot as
// the only other account, the rotation has no valid target.
func TestRotationWithOnlyAnAPIKeyLeftSwitchesNowhere(t *testing.T) {
	s := newTestStore(t, nil)
	ca := oauthCreds("acc-a", "ref-a")
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": oauthSeatEmail, "organizationUuid": ""}),
		"2": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
	}))
	seedBackup(t, s, "1", oauthSeatEmail, ca, "")
	seedBackup(t, s, "2", apiKeySeatEmail, apiKeySeatKey, "")
	seedLive(t, s, oauthSeatEmail, "", ca)
	before := seatState(t, s)

	res, err := Switch(s, nil, true, nil, nil)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	m, _ := res.(map[string]any)
	if m["switched"] != false || m["reason"] != "no-valid-target" {
		t.Errorf("Switch = %v, want no switch for want of a valid target", m)
	}
	assertSeatUnchanged(t, s, before)
}

// TestAnApprovalIsUsedUpByTheSwitchItWasGivenFor: an approval recorded for a
// subscription slot is consumed by the switch it was given for, so it cannot
// later let a switch onto an API-key account put into that slot through
// unasked (remove, then add-token into the same slot).
func TestAnApprovalIsUsedUpByTheSwitchItWasGivenFor(t *testing.T) {
	s := newTestStore(t, nil)
	approvalSeat(t, s)
	ApproveAPIKeySwitch("3") // slot 3 is a subscription account
	if _, err := SwitchTo(s, "3", true, false); err != nil {
		t.Fatalf("SwitchTo(3): %v", err)
	}
	if takeApproval("3") {
		t.Fatal("the approval for #3 survived the switch it was given for")
	}

	// The same holds when the switch writes nothing: the already-active
	// short-circuit consumes it too.
	ApproveAPIKeySwitch("3")
	if _, err := SwitchTo(s, "3", true, false); err != nil {
		t.Fatalf("SwitchTo(3) onto the active slot: %v", err)
	}
	if takeApproval("3") {
		t.Fatal("the approval for #3 survived an already-active switch")
	}

	// Slot 3 is then removed and an API key added in its place: a switch
	// onto it is refused, since no approval was left behind.
	if _, err := SwitchTo(s, "1", true, false); err != nil {
		t.Fatalf("SwitchTo(1): %v", err)
	}
	data, err := s.ReadSequence()
	if err != nil {
		t.Fatal(err)
	}
	data.Accounts["3"] = record(map[string]any{"email": "key-3@token.local", "organizationUuid": "", "kind": "api_key"})
	writeSeq(t, s, data)
	seedBackup(t, s, "3", "key-3@token.local", apiKeySeatKey, "")
	if _, err := SwitchTo(s, "3", true, false); err == nil || err.Error() != ErrAPIKeyNeedsApproval("3").Error() {
		t.Fatalf("SwitchTo(3) onto the new API key = %v, want the approval refusal", err)
	}
}

// TestSwitchToTheActiveAPIKeyAccountIsAlreadyOn: the API-key account already in
// use is reported as such, with no approval and nothing written; --force still
// needs one.
func TestSwitchToTheActiveAPIKeyAccountIsAlreadyOn(t *testing.T) {
	s := newTestStore(t, nil)
	approvalSeat(t, s)
	ApproveAPIKeySwitch("2")
	if _, err := SwitchTo(s, "2", true, false); err != nil {
		t.Fatalf("SwitchTo(2): %v", err)
	}
	liveBefore := readActiveCreds(t, s)
	res, err := SwitchTo(s, "2", true, false)
	if err != nil {
		t.Fatalf("SwitchTo(2) onto the active API key = %v, want already-active", err)
	}
	if m, _ := res.(map[string]any); m["reason"] != "already-active" {
		t.Errorf("result = %v, want already-active", res)
	}
	if got := readActiveCreds(t, s); got != liveBefore || got != apiKeySeatKey {
		t.Errorf("live credential = %q, want the key untouched", got)
	}
	if _, err := SwitchTo(s, "2", true, true); err == nil || err.Error() != ErrAPIKeyNeedsApproval("2").Error() {
		t.Errorf("SwitchTo(2) --force without an approval = %v, want the approval refusal", err)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	defer func() { os.Stdout = prev }()
	fn()
	os.Stdout = prev
	_ = w.Close()
	return <-done
}

// TestTheFollowupAfterAnAPIKeySwitchSaysRestart: after a switch onto an
// API-key account the note says the running sessions need a restart, not
// "no restart needed"; a switch back to a subscription account keeps the
// usual note.
func TestTheFollowupAfterAnAPIKeySwitchSaysRestart(t *testing.T) {
	s := newTestStore(t, nil)
	approvalSeat(t, s)
	ApproveAPIKeySwitch("2")
	out := captureStdout(t, func() {
		if _, err := SwitchTo(s, "2", false, false); err != nil {
			t.Errorf("SwitchTo(2): %v", err)
		}
	})
	if !strings.Contains(out, APIKeyRestartNote) || strings.Contains(out, "no restart needed") {
		t.Errorf("follow-up onto the API key:\n%s", out)
	}
	out = captureStdout(t, func() {
		if _, err := SwitchTo(s, "1", false, false); err != nil {
			t.Errorf("SwitchTo(1): %v", err)
		}
	})
	if strings.Contains(out, APIKeyRestartNote) || !strings.Contains(out, "no restart needed") {
		t.Errorf("follow-up onto the subscription account:\n%s", out)
	}
}

// TestTheFreshMachineSkipNamesTheAPIKeyRule: on a machine with no live login
// the bare switch skips a preferred API-key slot for the approval rule, not
// for missing credentials, and activates the next subscription account.
func TestTheFreshMachineSkipNamesTheAPIKeyRule(t *testing.T) {
	s := newTestStore(t, nil)
	writeSeq(t, s, seqData(ptrInt(1), []int{1, 2}, map[string]json.RawMessage{
		"1": record(map[string]any{"email": apiKeySeatEmail, "organizationUuid": "", "kind": "api_key"}),
		"2": record(map[string]any{"email": "b@x.com", "organizationUuid": ""}),
	}))
	seedBackup(t, s, "1", apiKeySeatEmail, apiKeySeatKey, "")
	seedBackup(t, s, "2", "b@x.com", oauthCreds("acc-b", "ref-b"), "")
	res, err := Switch(s, nil, true, nil, nil)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	m, _ := res.(map[string]any)
	warnings, _ := json.Marshal(m["warnings"])
	if !strings.Contains(string(warnings), "Skipped Account-1 (API key: switching to it changes how Claude Code authenticates)") ||
		strings.Contains(string(warnings), "no stored credentials") {
		t.Errorf("warnings = %s, want the API-key skip reason", warnings)
	}
	if to, _ := m["to"].(map[string]any); to["email"] != "b@x.com" {
		t.Errorf("activated %v, want #2", m["to"])
	}
}
