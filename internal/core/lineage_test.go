// The auto engine over the real store while ~/.claude.json names slot 6 but
// the credential file holds slot 5's lineage (DESIGN A68): slot 5 is live, its
// usage is fetched, and the engine does not fail over from it.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/autoswitch"
	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/settings"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/testutil"
	"github.com/tyclab/tycswap/internal/wincred"
)

func TestEngineStaysOnTheCredentialSlotUnderLineageMismatch(t *testing.T) {
	testutil.IsolateHome(t)
	testutil.Setenv(t, "NO_COLOR", "1")
	clk := testutil.FixedClock(t, "2026-07-17T09:00:00Z")
	var mu sync.Mutex
	calls := map[string]int{}
	fc := &oauth.FakeClient{UsageFn: func(_ context.Context, token string) (map[string]any, error) {
		mu.Lock()
		calls[token]++
		mu.Unlock()
		return map[string]any{
			"five_hour": map[string]any{"utilization": 10.0},
			"seven_day": map[string]any{"utilization": 10.0},
		}, nil
	}}
	sw, err := New(store.Options{Clock: clk, OAuth: fc, Keychain: keychain.NewFake(), WinCred: wincred.NewFake(), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	if err := sw.SetupDirectories(); err != nil {
		t.Fatalf("SetupDirectories: %v", err)
	}
	const email = "obi@example.com"
	identity := func(org string) string {
		return `{"oauthAccount":{"emailAddress":"` + email + `","organizationUuid":"` + org + `"}}`
	}
	for num, org := range map[string]string{"5": "org-five", "6": "org-six"} {
		if err := sw.WriteAccountCredentials(num, email, oauthCreds("tok-"+num)); err != nil {
			t.Fatal(err)
		}
		if err := sw.WriteAccountConfig(num, email, identity(org)); err != nil {
			t.Fatal(err)
		}
	}
	writeSeqDirect(t, sw, ptrInt(5), []int{5, 6}, map[string]json.RawMessage{
		"5": rawRecord(map[string]any{"email": email, "organizationUuid": "org-five"}),
		"6": rawRecord(map[string]any{"email": email, "organizationUuid": "org-six"}),
	})
	seedLive(t, sw, email, oauthCreds("tok-5"))
	if err := os.WriteFile(filepath.Join(sw.Home, ".claude.json"), []byte(identity("org-six")), 0o600); err != nil {
		t.Fatal(err)
	}

	var events []string
	engine := autoswitch.NewEngine(autoswitchAdapter{sw}, settings.Default(), func(e autoswitch.Event) {
		events = append(events, fmt.Sprintf("%T%+v", e, e))
	}, false, autoswitch.WithClock(clk), autoswitch.WithOAuthClient(fc), autoswitch.WithRNG(func() float64 { return 0.5 }))
	for tick := 1; tick <= 5; tick++ {
		if got := engine.Tick(); got != autoswitch.NoAction {
			t.Fatalf("tick %d = %v, want NoAction; events:\n%v", tick, got, events)
		}
		clk.Advance(2 * time.Minute)
	}
	if cur := sw.CurrentAccountNumber(); cur == nil || *cur != "5" {
		t.Fatalf("live slot = %v, want 5", cur)
	}
	if e := sw.UsageEntriesByAccount(map[string]bool{})["5"]; e.LastGood == nil || e.SkipReason != "" {
		t.Fatalf("slot 5 usage = %+v, want a measurement and no skip", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["tok-5"] == 0 {
		t.Fatalf("usage requests by token = %v, want slot 5's live token used", calls)
	}
}
