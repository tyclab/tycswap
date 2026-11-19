package web

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
)

// mythosOps is the fake account operations with the Mythos mark, as
// *core.Switcher has it (DESIGN A106).
type mythosOps struct{ *fakeOps }

func (o mythosOps) SetMythos(id string, allowed bool) error {
	o.record(fmt.Sprintf("SetMythos(%s,%v)", id, allowed))
	return o.err("SetMythos")
}

func withMythosOps() option {
	return func(h *harness, d *Deps) { d.Accounts = mythosOps{h.ops} }
}

// The routes set and clear the mark through the account operations; a Codex
// key is refused, and operations without the mark answer 503.
func TestMythosRoutes(t *testing.T) {
	h := newHarness(t, withMythosOps())
	for _, action := range []string{"allow", "disallow"} {
		resp := h.post("/api/accounts/claude:work/mythos/" + action)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", action, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if got := h.ops.Calls(); !reflect.DeepEqual(got, []string{"SetMythos(work,true)", "SetMythos(work,false)"}) {
		t.Fatalf("calls %v", got)
	}
	resp := h.post("/api/accounts/codex:1/mythos/allow")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Codex status %d", resp.StatusCode)
	}
	resp.Body.Close()
	for _, opt := range []option{withNoAccounts(), func(*harness, *Deps) {}} {
		h = newHarness(t, opt)
		resp = h.post("/api/accounts/claude:1/mythos/allow")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("missing operations status %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	h = newHarness(t, withMythosOps())
	for _, path := range []string{"/api/accounts/claude:1/mythos/allow", "/api/accounts/claude:1/mythos/disallow"} {
		if resp := h.do(h.newReq(http.MethodPost, path, nil)); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without the cookie: %d", path, resp.StatusCode)
		}
		if resp := h.do(h.withCookie(h.newReq(http.MethodPost, path, nil))); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s without CSRF: %d", path, resp.StatusCode)
		}
	}
	if got := h.ops.Calls(); len(got) != 0 {
		t.Fatalf("unauthenticated calls %v", got)
	}
}

// A row carries mythos only when the mark is set.
func TestStateRowMythosOnlyWhenMarked(t *testing.T) {
	for _, marked := range []bool{false, true} {
		row := accountRow(reporting.AccountSnapshot{Number: "1", Email: "a@example.com", Mythos: marked, Provider: reporting.ProviderClaude})
		if v, present := row["mythos"]; present != marked || marked && v != true {
			t.Fatalf("marked %v: row %v", marked, row)
		}
	}
}

const mythosScript = `'use strict';
const assert = require('node:assert/strict');
const js = require('node:fs').readFileSync(process.argv[2], 'utf8');
function block(start, end) {
  const a = js.indexOf(start), b = js.indexOf(end, a);
  assert.ok(a >= 0 && b > a);
  return js.slice(a, b);
}
const ctx = {el: (tag, attrs) => attrs, chip: (text) => text, SENTINEL_STATUSES: {}};
require('node:vm').runInNewContext(
  block('  function isClaude(', '  // rowKey ') +
  block('  function rowKey(', '  function accountLabel(') +
  block('  function statusChips(', '  function usageFreshness(') +
  block('  function menuButton(', '  function modelMeter('), ctx);
for (const mythos of [false, true]) {
  const row = {provider: 'claude', number: 2, key: 'claude:2', mythos, switchable: true};
  const action = ctx.mythosAction(row);
  assert.equal(action['data-post'], '/api/accounts/claude%3A2/mythos/' + (mythos ? 'disallow' : 'allow'));
  assert.equal(action.text, mythos ? 'Disallow for Mythos' : 'Allow for Mythos');
  assert.equal(ctx.statusChips(row).includes('Mythos'), mythos);
  row.provider = 'codex'; row.key = 'codex:2';
  assert.equal(ctx.mythosAction(row), null);
  assert.equal(ctx.statusChips(row).includes('Mythos'), false);
}
const unmarked = ctx.statusChips({provider: 'claude', number: 3, key: 'claude:3', switchable: true});
assert.equal(unmarked.includes('Mythos'), false);
`

func TestDashboardMythosControls(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app, script := filepath.Join(dir, "app.js"), filepath.Join(dir, "mythos.cjs")
	for path, body := range map[string][]byte{app: src, script: []byte(mythosScript)} {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(node, script, app).CombinedOutput(); err != nil {
		t.Fatalf("Mythos controls: %v\n%s", err, out)
	}
}
