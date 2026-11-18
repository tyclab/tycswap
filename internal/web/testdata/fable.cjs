'use strict';
// Runs the production header strip, account table and Auto tab over a small
// DOM (DESIGN A104): with a Fable store every Fable element is there, and
// Use for Fable posts the store's switch; without one there is none.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const js = fs.readFileSync(process.argv[2], 'utf8');
const document = {activeElement: null, hidden: false, listeners: {}};
class Node {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.attrs = {}; this.events = {}; this.style = {}; this.className = ''; this.hidden = false; this.disabled = false; this.checked = false; this._text = ''; this.title = '';
    this.classList = {add: c => { this.className = (this.className + ' ' + c).trim(); }, contains: c => this.className.split(' ').includes(c), toggle() {}, remove() {}}; }
  appendChild(n) { if (n.parentNode) n.parentNode.removeChild(n); this.children.push(n); n.parentNode = this; return n; }
  removeChild(n) { this.children.splice(this.children.indexOf(n), 1); n.parentNode = null; }
  get firstChild() { return this.children[0] || null; }
  get textContent() { return this._text + this.children.map(c => c.textContent).join(''); }
  set textContent(v) { this.children = []; this._text = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(k, fn) { (this.events[k] ||= []).push(fn); }
  click() { if (this.disabled) return; const ev = {target: this, preventDefault() {}}; for (const f of document.listeners.click || []) f(ev); }
  closest(sel) { for (let n = this; n; n = n.parentNode) { if (n.tagName === 'BUTTON' && sel.split(',').some(s => n.getAttribute(s.trim().match(/\[(.+)\]/)[1]) !== null)) return n; } return null; }
  cloneNode() { const c = new Node(this.tagName); c._text = this.textContent; c.className = this.className; return c; }
  all() { return this.children.flatMap(c => [c, ...c.all()]); }
}
document.createElement = tag => new Node(tag);
document.createTextNode = text => Object.assign(new Node('#text'), {_text: String(text)});
document.addEventListener = (k, fn) => { (document.listeners[k] ||= []).push(fn); };
document.querySelector = () => null;
document.querySelectorAll = () => [];
const ids = {};
for (const id of ['hdr-acct', 'accounts-body', 'accounts-empty', 'add-callout', 'add-callout-email', 'add-hint', 'accounts-tbl', 'add-current-codex', 'accounts-sub',
  'auto-badge', 'auto-body', 'auto-state', 'auto-counting', 'quarantine-body', 'quarantine-section', 'nextbest-list', 'nextbest-sub', 'nextbest-empty', 'nextbest-tbl', 'toasts']) { ids[id] = new Node('div'); }
ids['auto-switch'] = new Node('label').appendChild(new Node('input'));
const requests = [];
const ctx = {document, console, Promise, setTimeout, Math, Date, JSON, Object, Array, String, Number, isNaN, parseFloat, parseInt, encodeURIComponent,
  state: null, minimumStateSequence: 0, lastStateAt: 0, inflight: {}, accountOrder: {key: 'number', direction: 1}, $: id => ids[id], NAME: 'tycswap', CSRF: 'test', showTokenStatus: false,
  closeMenus() {}, toast() {},
  fetch: (url, opts) => { requests.push([opts.method, url]); return Promise.resolve({ok: true, status: 200, text: () => Promise.resolve('{"ok":true}')}); }};
function block(start, end, head = '') { const a = js.indexOf(start), b = js.indexOf(end, a); assert.ok(a >= 0 && b > a, start); vm.runInNewContext(head + js.slice(a, b), ctx); }
block('  var UNSAFE_ATTR', '  function toast(');
block('  function api(', '  // ---- tabs');
block('  // ---- shared derivations', '  // ---- header + summary');
block('  // fallbackNotices:', '  function renderSummary(');
block('  // ---- accounts', '  // ---- sessions');
block('  // ---- auto: render', '  // The Auto-switch switch:');
block('  function codexRestartNote(', '  function showUpdates(');
block("    var btn = ev.target.closest ? ev.target.closest('button[data-post], button[data-action]')", "  $('token-status-toggle')", "  document.addEventListener('click', function (ev) {\n");

const usage = {sevenDay: {pct: 40, resetsAt: new Date(Date.now() + 86400000).toISOString()}, scoped: [{name: 'Fable', pct: 20}]};
const row = (n, extra) => Object.assign({number: n, key: 'claude:' + n, provider: 'claude', email: 'acct' + n + '@example.com', kind: 'oauth',
  switchable: true, rotationEligible: true, usageStatus: 'ok', usage}, extra);
const auto = {available: true, running: true, owner: 'timer', lastTickAt: Date.now() / 1000, settings: {}, quarantine: {},
  counting: {mode: 'sessions', models: [], sessions: [], unservable: [], reserved: [], caption: 'counting 5h + 7d'}};
function fableState() {
  return {
    activeNumber: 1, settings: [],
    accounts: [row(1, {isActive: true}), row(2, {fableLogin: {live: true, status: 'ok'}}), row(3, {fableLogin: {live: false, status: 'ok'}}),
      row(4, {fableLogin: {live: false, status: 'sign in again'}}), row(5)],
    fable: {state: 'on', number: 2, email: 'acct2@example.com', storeDir: '/x/fable/store', source: 'backup'},
    auto: Object.assign({}, auto, {fable: {lastTickAt: null, caption: 'Counting 5h + 7d + Fable', ranking: ['3'], quarantine: {}}}),
  };
}
function plainState() {
  const st = fableState();
  delete st.fable; delete st.auto.fable;
  st.accounts.forEach(a => delete a.fableLogin);
  return st;
}
const text = n => n.textContent;
const rowOf = n => ids['accounts-body'].children.find(tr => tr.getAttribute('data-key') === 'claude:' + n);
const titleOf = e => e.getAttribute('title') || e.title;
const chips = n => rowOf(n).all().filter(e => e.className.startsWith('chip')).map(e => [text(e), e.className, titleOf(e)]);
const buttons = n => rowOf(n).all().filter(e => e.tagName === 'BUTTON' && e.getAttribute('data-label') === 'Use for Fable');
function render(st) {
  ctx.state = st;
  ids['hdr-acct'].removeAttribute('data-sig');
  ctx.renderActiveStrip(st); ctx.renderAccounts(st); ctx.renderAuto(st);
}

// With a Fable store: the header's second line, the chips, the buttons, the
// menu note and the Auto tab's line.
render(fableState());
assert.match(text(ids['hdr-acct']), /Fable: #2 acct2@example\.com/);
assert.deepEqual(chips(2).filter(c => c[0].startsWith('Fable')).map(c => c[0]), ['Fable']);
assert.deepEqual(chips(3).filter(c => c[0].startsWith('Fable')).map(c => c[0]), ['Fable login']);
const dead = chips(4).find(c => c[0] === 'Fable login: sign in again');
assert.ok(dead && dead[1].includes('chip-warn') && dead[2] === 'Sign in again: tycswap add --login --lineage fable --slot 4', String(dead));
for (const n of [1, 5]) { assert.equal(chips(n).filter(c => c[0].startsWith('Fable')).length, 0, '#' + n + ' has no Fable login'); }
assert.deepEqual(buttons(2).map(b => [text(b), b.disabled]), [['Fable here', true]]);
assert.deepEqual(buttons(3).map(b => [text(b), b.disabled]), [['Use for Fable', false]]);
for (const n of [1, 4, 5]) { assert.equal(buttons(n).length, 0, '#' + n + ' has no usable Fable login'); }
const note = n => rowOf(n).all().filter(e => e.className === 'menu-note').map(text);
assert.deepEqual(note(5), ['Add a Fable login: tycswap add --login --lineage fable --slot 5']);
assert.deepEqual(note(3), []);
assert.equal(text(ids['auto-body'].children[0]), 'Fable store: counting 5h + 7d + Fable · next #3');

// The store off: the header says so and names the command; the unusable
// store warns.
const off = fableState(); off.fable = {state: 'off', number: null, storeDir: '/x/fable/store', source: null, reason: 'the store is empty'};
render(off);
const offLine = ids['hdr-acct'].all().find(e => e.className === 'hdr-fable');
assert.equal(text(offLine), 'Fable store off');
assert.equal(titleOf(offLine), 'Turn it on: tycswap switch --store fable <account>');
const unusable = fableState(); unusable.fable.state = 'unusable'; unusable.fable.reason = "Account-2's Fable login: sign in again";
render(unusable);
assert.match(text(ids['hdr-acct']), /Fable: #2 acct2@example\.com.*Fable login: sign in again/);

// Without a Fable login nothing of it shows.
render(plainState());
for (const id of ['hdr-acct', 'accounts-body', 'auto-body']) {
  const all = [ids[id], ...ids[id].all()];
  assert.ok(!all.some(e => /Fable (store|login|here)|for Fable|Fable:/.test(e._text) || e.className.startsWith('chip') && text(e) === 'Fable' ||
    /fable/.test(e.className + ' ' + (e.attrs['data-post'] || '') + ' ' + (e.attrs.id || ''))), id + ' shows Fable without a Fable login: ' + text(ids[id]));
}

(async () => {
  // Use for Fable posts the Fable store's switch for its row.
  render(fableState());
  buttons(3)[0].click();
  buttons(2)[0].click();
  await new Promise(r => setTimeout(r, 0));
  assert.deepEqual(requests, [['POST', '/api/switch/claude%3A3?store=fable']]);
  console.log('fable: header, chips, Use for Fable, menu note and Auto line passed');
})().catch(e => { console.error(e); process.exitCode = 1; });
