package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// windowCell: usage unknown → the dashed "no data" meter; a window Anthropic
// did not report → the muted absent mark; a reported window → its meter.
const absentWindowScript = `'use strict';
const assert = require('node:assert');
const fs = require('node:fs');
const vm = require('node:vm');
const js = fs.readFileSync(process.argv[2], 'utf8');
function block(start, end) {
  const a = js.indexOf(start), b = js.indexOf(end, a);
  assert.ok(a >= 0 && b > a, start);
  return js.slice(a, b);
}
const ctx = {
  meter: o => ({meter: o}),
  emptyMeter: (label, size) => ({empty: label, size}),
  el: (tag, attrs, kids) => ({tag, attrs, kids}),
};
vm.runInNewContext(block('  // windowCell renders', '  // countdownText renders'), ctx);
assert.deepEqual(ctx.windowCell(null, 'fiveHour', '5h'), {empty: '5h', size: undefined});
assert.deepEqual(ctx.windowCell({fiveHour: {pct: null}}, 'fiveHour', '5h'), {meter: {label: '5h', pct: null, resetsAt: undefined, size: undefined}});
const absent = ctx.windowCell({sevenDay: {pct: 15, resetsAt: 'x'}}, 'fiveHour', '5h', 'compact');
assert.equal(absent.attrs.class, 'meter meter-absent compact');
assert.match(absent.attrs.title, /no 5h window/);
assert.equal(absent.kids[0].kids[1].attrs.text, '—');
assert.deepEqual(ctx.windowCell({fiveHour: {pct: 56, resetsAt: 'r'}}, 'fiveHour', '5h'), {meter: {label: '5h', pct: 56, resetsAt: 'r', size: undefined}});
`

func TestAppJSAbsentWindowIsNotNoData(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; dashboard absent-window test skipped")
	}
	src, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app, script := filepath.Join(dir, "app.js"), filepath.Join(dir, "absentwindow.cjs")
	for path, body := range map[string][]byte{app: src, script: []byte(absentWindowScript)} {
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(node, script, app).CombinedOutput(); err != nil {
		t.Fatalf("dashboard absent window: %v\n%s", err, out)
	}
	html, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	css, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`class="c-meter col-5h"`} {
		if !contains(string(html), want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	if !contains(string(css), ".acct-tbl.no-5h-column .c-m5, .acct-tbl.no-5h-column .col-5h { display: none; }") {
		t.Error("style.css does not hide the 5-hour column")
	}
	if !contains(string(src), "classList.toggle('no-5h-column'") {
		t.Error("app.js never toggles the 5-hour column")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
