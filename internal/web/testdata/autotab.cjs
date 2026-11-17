'use strict';
// Runs the production Auto tab and Settings rows over a small DOM (DESIGN
// A93): the one switch, the heading's on/off and who checks, Counting now
// and the autoswitch.model warning, and every name a tile or toast shows
// taken from the registry labels the Go test passes in.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const js = fs.readFileSync(process.argv[2], 'utf8');
const registry = JSON.parse(process.argv[3]); // [{key, label}] from settings.SettingSpecs
const document = {activeElement: null, hidden: false};
class Node {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.attrs = {}; this.events = {}; this.style = {}; this.className = ''; this.hidden = false; this.disabled = false; this.checked = false; this._text = ''; this.title = '';
    this.classList = {add: c => { this.className = (this.className + ' ' + c).trim(); }, contains: c => this.className.split(' ').includes(c), toggle() {}}; }
  appendChild(n) { if (n.parentNode) n.parentNode.removeChild(n); this.children.push(n); n.parentNode = this; return n; }
  removeChild(n) { this.children.splice(this.children.indexOf(n), 1); n.parentNode = null; }
  get firstChild() { return this.children[0] || null; }
  get textContent() { return this._text + this.children.map(c => c.textContent).join(''); }
  set textContent(v) { this.children = []; this._text = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(k, fn) { (this.events[k] ||= []).push(fn); }
  emit(k) { for (let n = this; n; n = n.parentNode) for (const f of n.events[k] || []) f.call(n, {target: this, key: k, preventDefault() {}}); }
  click() { if (!this.disabled) this.emit('click'); }
  cloneNode() { const c = new Node(this.tagName); c._text = this.textContent; c.className = this.className; return c; }
  contains(n) { return this === n || this.children.some(c => c.contains(n)); }
  all() { return this.children.flatMap(c => [c, ...c.all()]); }
}
document.createElement = tag => new Node(tag);
document.createTextNode = text => Object.assign(new Node('#text'), {_text: String(text)});
document.body = new Node('body'); document.activeElement = document.body;
const ids = {};
for (const id of ['auto-badge', 'auto-body', 'auto-model-warning', 'auto-state', 'auto-counting', 'quarantine-body', 'quarantine-section',
  'nextbest-list', 'nextbest-sub', 'nextbest-empty', 'nextbest-tbl', 'badge-auto', 'settings-grid', 'settings-empty', 'settings-sub', 'toasts', 'conn-age']) { ids[id] = new Node('div'); }
const switchLabel = new Node('label');
ids['auto-switch'] = switchLabel.appendChild(new Node('input'));
const toasts = [], requests = [];
const ctx = {document, console, Promise, setTimeout, Math, Date, JSON, Object, Array, String, Number, isNaN, parseFloat, parseInt, encodeURIComponent,
  state: null, minimumStateSequence: 0, lastStateAt: 0, $: id => ids[id], NAME: 'tycswap', CSRF: 'test', showTokenStatus: false,
  fetch: (url, opts) => { requests.push([opts.method, url]); return Promise.resolve({ok: true, status: 200, text: () => Promise.resolve('{"ok":true}')}); }};
function block(start, end) { const a = js.indexOf(start), b = js.indexOf(end, a); assert.ok(a >= 0 && b > a, start); vm.runInNewContext(js.slice(a, b), ctx); }
block('  var UNSAFE_ATTR', '  // ---- tabs');
ctx.toast = (msg, kind) => toasts.push([msg, kind]);
block('  // ---- shared derivations', '  // ---- header + summary');
block('  // fallbackNotices:', '  // renderActiveStrip:');
block('  // ---- auto: render', '  // ---- settings (shared row renderer)');
block('  // ---- settings (shared row renderer)', '  // ---- guarded section rendering');

const label = key => registry.find(r => r.key === key).label;
const settings = registry.map(r => ({key: r.key, label: r.label, kind: 'float', value: 90, default: 90, isDefault: true}));
const owner = 'flakelab-tycswap-autoswitch.timer';
function auto(mode, on, hosted) {
  const counting = {mode, models: mode === 'setting' ? ['Fable'] : [], sessions: [], unservable: [], reserved: [], caption: 'counting 5h + 7d (' + mode + ')'};
  if (mode === 'setting') { counting.setting = 'Fable'; }
  return {available: true, running: on, hosted, owner: hosted ? '' : owner, lastTickAt: Date.now() / 1000 - 120, settings: {'autoswitch.intervalSeconds': 60},
    counting, quarantine: {}, codex: {enabled: true, running: on, threshold: 97, lastTick: null}};
}
const text = node => node.textContent;
const words = () => [ids['auto-badge'], ids['auto-state'], ids['auto-counting'], ids['auto-body']].map(text).join(' ');

// Rows 1 and 7: on/off in both modes, the caption and the warning for the
// three counting modes.
const want = {
  'owner on': ['on', 'checked by ' + owner + ', last check 2m ago'],
  'owner off': ['off', 'not checked while off, last check 2m ago'],
  'hosted on': ['on', 'checked every 60 s by this dashboard'],
  'hosted off': ['off', 'not checked while off'],
};
for (const mode of ['setting', 'sessions', 'hosted']) {
  for (const [name, [chipText, line]] of Object.entries(want)) {
    const st = {accounts: [], settings, auto: auto(mode, name.endsWith(' on'), name.startsWith('hosted'))};
    ctx.state = st;
    ctx.renderAuto(st);
    ctx.renderAutoBadge(st);
    assert.equal(text(ids['auto-badge']), chipText, name);
    assert.equal(text(ids['auto-state']), line, name);
    assert.equal(text(ids['badge-auto']), chipText, name + ': the tab badge follows the heading');
    assert.ok(ids['badge-auto'].title.endsWith(line), name + ': badge title ' + ids['badge-auto'].title);
    assert.equal(ids['auto-switch'].checked, name.endsWith(' on'), name + ': the switch follows the state');
    assert.equal(text(ids['auto-counting']), 'Counting now: 5h + 7d (' + mode + ')', mode + ': the label says counting once');
    const warning = ids['auto-model-warning'];
    if (mode === 'setting') {
      assert.equal(warning.hidden, false, 'the warning shows while autoswitch.model is set');
      for (const part of ['"Fable"', 'session following is off', 'tycswap config unset autoswitch.model']) { assert.ok(text(warning).includes(part), text(warning)); }
    } else {
      assert.equal(warning.hidden, true, mode + ': no warning without autoswitch.model');
    }
    if (name.startsWith('owner')) { assert.doesNotMatch(words(), /engine|running/i, name + ': ' + words()); }
  }
}

// Row 10: the tiles carry the registry's names.
const tiles = ids['auto-body'].all().filter(n => n.className === 'tile-label').map(text);
assert.deepEqual(tiles, ['autoswitch.fiveHourThreshold', 'autoswitch.sevenDayThreshold', 'autoswitch.modelThreshold', 'autoswitch.strategy', 'autoswitch.codexEnabled'].map(label));
const codexSub = ids['auto-body'].all().filter(n => n.className === 'tile-sub').map(text).pop();
assert.ok(codexSub.startsWith(label('autoswitch.codexThreshold') + ' 97%'), codexSub);

(async () => {
  // Row 2: the one switch posts what Start and Stop did, and says so in the
  // registry's name.
  const toggle = ids['auto-switch'];
  toggle.checked = true; toggle.emit('change');
  await new Promise(r => setTimeout(r, 0));
  toggle.checked = false; toggle.emit('change');
  await new Promise(r => setTimeout(r, 0));
  assert.deepEqual(requests.splice(0), [['POST', '/api/auto/start'], ['POST', '/api/auto/stop']]);
  assert.deepEqual(toasts.splice(0).map(t => t[0]), [label('autoswitch.enabled') + ' on.', label('autoswitch.enabled') + ' off.']);

  // Row 10: a Save and a Reset toast name the setting as the registry does.
  ctx.renderSettings({settings});
  const buttons = ids['settings-grid'].all().filter(n => n.tagName === 'BUTTON');
  for (const b of buttons) { b.click(); }
  await new Promise(r => setTimeout(r, 0));
  const named = toasts.map(t => t[0]);
  for (const r of registry) {
    assert.ok(named.includes('Save ' + r.label + ' done.'), 'save toast for ' + r.key + ': ' + named.join(' | '));
  }
  const rows = ids['settings-grid'].all().filter(n => n.className === 'label').map(text);
  assert.deepEqual(rows, registry.map(r => r.label), 'each row carries the registry name');
  console.log('auto tab: one switch, heading, counting, warning and registry names passed');
})().catch(e => { console.error(e); process.exitCode = 1; });
