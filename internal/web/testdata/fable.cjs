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
  'auto-badge', 'auto-body', 'auto-state', 'auto-counting', 'quarantine-body', 'quarantine-section', 'nextbest-list', 'nextbest-sub', 'nextbest-empty', 'nextbest-tbl', 'toasts',
  'sess-filter', 'sess-status', 'sess-sort', 'next-mythos']) { ids[id] = new Node('div'); }
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
block('  // ---- sessions', '  // ---- auto: render');
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

if (process.argv[3] === 'editor') {
  for (const id of ['modal','modal-form','modal-title','modal-message','modal-error','modal-fields','modal-ok','modal-cancel']) ids[id] = new Node('div');
  Node.prototype.focus = function () { document.activeElement = this; };
  Node.prototype.querySelector = function () { return this.all().find(n => n.tagName === 'INPUT'); };
  ctx.$ = id => ids[id] || ids['modal-fields'].all().find(n => n.getAttribute('id') === id);
  block('  var modal =', '  // ---- shared derivations');
  block('  function showUpdates(', "  document.addEventListener('click'");
  const st = plainState();
  st.accounts[0].models = ['opus'];
  render(st);
  const edit = rowOf(1).all().find(b => b.tagName === 'BUTTON' && /edit models/i.test(text(b)));
  assert.ok(edit, 'a known allocation needs an editor');
  edit.click();
  const fields = ids['modal-fields'].all().filter(n => n.tagName === 'INPUT');
  const field = name => fields.find(n => n.getAttribute('name') === name);
  assert.equal(field('opus').checked, true);
  field('mythos').checked = true;
  ctx.fetch = async (url, opts) => {
    assert.equal(url, '/api/accounts/claude%3A1/models');
    assert.deepEqual(JSON.parse(opts.body).models.sort(), ['mythos','opus']);
    return {ok: false, status: 409, text: async () => '{"error":"readonly fixture: store busy"}'};
  };
  ids['modal-form'].events.submit[0]({preventDefault(){}});
  setImmediate(() => {
    assert.match(text(ids['modal-error']), /store busy/);
    assert.notEqual(ids.modal.getAttribute('open'), null, 'refusal closed the editor');
    assert.ok(field('opus').checked && field('mythos').checked, 'refusal erased input');
  });
 } else if (process.argv[3] === 'session-owner') {
  for (const id of ['sessions-groups','sessions-stats','sessions-empty','ide-list','ide-count','ide-empty','ide-tbl']) ids[id] = new Node('div');
  document.querySelector = () => new Node('div');
  ctx.sessFilter = {q: '', status: '', sort: 'busy'};
  ctx.openGroups = {};
  ctx.state = fableState();
  const message = 'The session login is refreshing.';
  ctx.renderSessions({claude: [
    {pid: 31, sessionId: 'refreshing', cwd:'/fixture', status:'busy', store:'default', login:'Mythos login', accountNumber:'8', movesError:message},
    {pid: 32, sessionId: 'fresh', cwd:'/fixture', status:'busy', login:'Fable store', accountNumber:'unknown'},
    {pid: 33, sessionId: 'normal', cwd:'/fixture', status:'busy', login:'default login', accountNumber:'3'},
  ]});
  const body = ids['sessions-groups'].all().find(n => n.tagName === 'TBODY');
  const cells = body.children.filter(n => n.tagName === 'TR');
  const ix = cells.findIndex(r => text(r).includes(message));
  assert.ok(ix > 0 && cells[ix].children.length === 1 && cells[ix].children[0].getAttribute('colspan') === '6',
    'put one refresh warning in its own full-width row below the session');
  assert.ok(text(cells[ix-1]).includes('Mythos login · #8'));
  assert.ok(text(body).includes('Fable store · unknown'));
  assert.ok(text(body).includes('default login · #3'));
} else if (process.argv[3] === 'mythos') {
  const st = plainState();
  const longEmail = 'long'.repeat(25) + '@example.com';
  st.mythos = {number: 1, email: longEmail, linkedSessions: 2, countedWindows: ['5h', '7d', 'Mythos']};
  st.accounts[0].email = longEmail;
  st.accounts[0].mythosLogin = {live: true, selectable: true, status: 'ok'};
  st.accounts[1].mythosLogin = {live: false, selectable: true, status: 'ok'};
  st.accounts[1].models = ['opus', 'fable'];
  st.accounts[2].mythosLogin = {live: false, selectable: false, status: 'sign in again'};
  render(st);
  const header = text(ids['hdr-acct']);
  assert.match(header, /Mythos login.*#1/);
  for (const value of [longEmail, '5h', '7d', '2 linked sessions']) assert.ok(header.includes(value), header);
  const mythosLine = ids['hdr-acct'].all().find(e => e.className === 'hdr-mythos');
  assert.ok(!mythosLine.all().some(e => e.classList.contains('ellipsis') && text(e).includes(longEmail)),
    'the Mythos account must remain readable when the header is narrow');
  const labels = n => rowOf(n).all().filter(e => e.tagName === 'BUTTON').map(text);
  assert.ok(labels(2).includes('Use for Mythos'));
  for (const n of [1,3,4]) assert.ok(!labels(n).includes('Use for Mythos'));
  assert.ok(chips(1).some(c => c[0] === 'Mythos login: in use'), 'selected Mythos login needs an explicit label');
  assert.ok(chips(2).some(c => c[0] === 'Mythos login'));
  assert.ok(chips(3).some(c => c[0] === 'Mythos login: sign in again'));
  assert.ok(labels(2).some(l => /edit models/i.test(l)));
  assert.ok(!labels(3).some(l => /edit models/i.test(l)), 'unreadable models cannot be overwritten');
  const moves = c => ctx.sessionRow(c).all().filter(e => e.tagName === 'BUTTON' && text(e).startsWith('Move to')).map(text);
  assert.deepEqual(moves({pid: 12, sessionId: 'test', moves: ['mythos', 'fable', 'default']}), ['Move to Mythos login','Move to Fable store','Move to default login']);
  assert.deepEqual(moves({pid: 13, sessionId: 'legacy'}), []);
  process.exit(0);
}

if (!process.argv[3]) {
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
// An API-key account takes no Fable login (A101): its menu names no command.
const keyed = fableState(); keyed.accounts[4].kind = 'api_key';
render(keyed);
assert.deepEqual(note(5), []);
// The header redraws when only the Fable store moved.
render(fableState());
const moved = fableState(); moved.fable.number = 3; moved.fable.email = 'acct3@example.com';
ctx.renderActiveStrip(moved);
assert.match(text(ids['hdr-acct']), /Fable: #3 acct3@example\.com/);
// Only a scheduler's `auto --once` runs the Fable tick (DESIGN A102): while
// this dashboard runs the checks itself the line names no next account.
const hosted = fableState(); hosted.auto.owner = ''; hosted.auto.hosted = true;
render(hosted);
assert.equal(text(ids['auto-body'].children[0]), "Fable store: this dashboard's checks do not move it; a scheduler running tycswap auto --once does");
// Nor while no Fable tick acts on it: auto-switch off, or the store off
// (only a switch by hand turns it on, DESIGN A100).
const autoOff = fableState(); autoOff.auto.running = false;
render(autoOff);
assert.equal(text(ids['auto-body'].children[0]), 'Fable store: not checked while off');

// The store off: the header says so and names the command; the unusable
// store warns.
const off = fableState(); off.fable = {state: 'off', number: null, storeDir: '/x/fable/store', source: null, reason: 'the store is empty'};
render(off);
const offLine = ids['hdr-acct'].all().find(e => e.className === 'hdr-fable');
assert.equal(text(offLine), 'Fable store off');
assert.equal(titleOf(offLine), 'Turn it on: tycswap switch --store fable <account>');
assert.equal(text(ids['auto-body'].children[0]), 'Fable store: off; tycswap switch --store fable <account> turns it on');
const parked = fableState();
parked.fable = {state: 'parked', number: null, startup: 'parked, waiting for an eligible native Fable start'};
render(parked);
assert.equal(text(ids['hdr-acct'].all().find(e => e.className === 'hdr-fable')), 'Fable store: ' + parked.fable.startup);
assert.equal(text(ids['auto-body'].children[0]), 'Fable store: ' + parked.fable.startup);
parked.fable.startup = 'Account-2 ready for the next native Fable start';
delete parked.auto.fable;
ctx.renderActiveStrip(parked);
ctx.renderAuto(parked);
assert.equal(text(ids['hdr-acct'].all().find(e => e.className === 'hdr-fable')), 'Fable store: ' + parked.fable.startup);
assert.equal(text(ids['auto-body'].children[0]), 'Fable store: ' + parked.fable.startup);
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

// The Sessions tab names a session's store where its status line recorded
// it: the Fable store, the default login, or, while an account has a Fable
// login and no record says, nothing. Without a Fable login every session is
// on the default login.
const storeChips = c => ctx.sessionRow(c).all().filter(e => e.className.startsWith('chip') && /Fable store|default login/.test(text(e))).map(text);
const session = store => ({pid: 7, sessionId: 'abcdef12-0000', status: 'busy', store});
ctx.state = fableState();
assert.deepEqual(storeChips(session('fable')), ['Fable store']);
assert.deepEqual(storeChips(session('default')), ['default login']);
assert.deepEqual(storeChips(session(undefined)), [], 'an unknown store is not named');
ctx.state = plainState();
assert.deepEqual(storeChips(session(undefined)), ['default login']);

(async () => {
  // Use for Fable posts the Fable store's switch for its row.
  render(fableState());
  buttons(3)[0].click();
  buttons(2)[0].click();
  await new Promise(r => setTimeout(r, 0));
  assert.deepEqual(requests, [['POST', '/api/switch/claude%3A3?store=fable']]);
  console.log('fable: header, chips, Use for Fable, menu note and Auto line passed');
})().catch(e => { console.error(e); process.exitCode = 1; });
}
