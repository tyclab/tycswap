package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAppJSHierarchicalRanking(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; dashboard ranking test skipped")
	}
	src, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.js")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "testdata/ranking.cjs", path).CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard ranking: %v\n%s", err, out)
	}
}

func TestDashboardTheme(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, err := staticFS.ReadFile("static/theme.js")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "theme.js")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "testdata/theme.cjs", path).CombinedOutput(); err != nil {
		t.Fatalf("theme: %v\n%s", err, out)
	}
}

const usageSkipScript = `'use strict';
const assert = require('node:assert/strict');
const js = require('node:fs').readFileSync(process.argv[2], 'utf8');
const a = js.indexOf('  function usageFreshness('), b = js.indexOf('  function menuButton(', a);
assert.ok(a >= 0 && b > a);
const ctx = {el: (tag, attrs) => attrs, Date};
require('node:vm').runInNewContext(js.slice(a, b), ctx);
const reason = 'owner holds an expired token; Claude Code refreshes it on next use';
for (const usageStatus of ['token_expired', 'relogin_required']) {
  const chip = ctx.usageFreshness({usageStatus, usageRefresh: {skipReason: reason, skipAt: '2026-10-25T17:00:19Z'}});
  assert.equal(chip.text, reason, usageStatus);
  assert.ok(chip.title.includes(reason) && /Usage is cached between requests/.test(chip.title), chip.title);
}
assert.match(ctx.usageFreshness({usageStatus: 'token_expired', usageRefresh: {}}).text, /Waiting for Claude/);
`

func TestAppJSUsageChipNamesSkipReason(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; dashboard usage chip test skipped")
	}
	src, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app, script := filepath.Join(dir, "app.js"), filepath.Join(dir, "usageskip.cjs")
	for path, body := range map[string][]byte{app: src, script: []byte(usageSkipScript)} {
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(node, script, app).CombinedOutput(); err != nil {
		t.Fatalf("dashboard usage chip: %v\n%s", err, out)
	}
}
