package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const startLoginScript = `'use strict';
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
for (const startLogin of [false, true]) {
  const row = {provider: 'claude', number: 2, key: 'claude:2', startLogin, switchable: true};
  const action = ctx.startLoginAction(row);
  assert.equal(action['data-post'], '/api/accounts/claude%3A2/start-login/' + (startLogin ? 'disallow' : 'allow'));
  assert.equal(action.text, startLogin ? 'Disallow for start' : 'Allow for start');
  assert.equal(ctx.statusChips(row).includes('start login'), startLogin);
  row.provider = 'codex'; row.key = 'codex:2';
  assert.equal(ctx.startLoginAction(row), null);
  assert.equal(ctx.statusChips(row).includes('start login'), false);
}
`

func TestDashboardStartLoginControls(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app, script := filepath.Join(dir, "app.js"), filepath.Join(dir, "startlogin.cjs")
	for path, body := range map[string][]byte{app: src, script: []byte(startLoginScript)} {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(node, script, app).CombinedOutput(); err != nil {
		t.Fatalf("start login controls: %v\n%s", err, out)
	}
}
