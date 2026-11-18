'use strict';
// Runs the production account table over a small DOM: a row with an alias
// offers Remove alias beside Change alias…, which posts the alias route with
// no body (an empty alias); a row without one offers Set alias… only.
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
for (const id of ['accounts-body', 'accounts-empty', 'add-callout', 'add-callout-email', 'add-hint', 'accounts-tbl', 'add-current-codex', 'accounts-sub', 'toasts']) { ids[id] = new Node('div'); }
const requests = [];
const ctx = {document, console, Promise, setTimeout, Math, Date, JSON, Object, Array, String, Number, isNaN, parseFloat, parseInt, encodeURIComponent,
  state: null, minimumStateSequence: 0, lastStateAt: 0, inflight: {}, accountOrder: {key: 'number', direction: 1}, $: id => ids[id], NAME: 'tycswap', CSRF: 'test', showTokenStatus: false,
  closeMenus() {}, toast() {},
  fetch: (url, opts) => { requests.push([opts.method, url, opts.body]); return Promise.resolve({ok: true, status: 200, text: () => Promise.resolve('{"ok":true}')}); }};
function block(start, end, head = '') { const a = js.indexOf(start), b = js.indexOf(end, a); assert.ok(a >= 0 && b > a, start); vm.runInNewContext(head + js.slice(a, b), ctx); }
block('  var UNSAFE_ATTR', '  function toast(');
block('  function api(', '  // ---- tabs');
block('  // ---- shared derivations', '  // ---- header + summary');
block('  // ---- accounts', '  // ---- sessions');
block('  function codexRestartNote(', '  function showUpdates(');
block("    var btn = ev.target.closest ? ev.target.closest('button[data-post], button[data-action]')", "  $('token-status-toggle')", "  document.addEventListener('click', function (ev) {\n");

const row = (n, extra) => Object.assign({number: n, key: 'claude:' + n, provider: 'claude', email: 'acct' + n + '@example.com', kind: 'oauth',
  switchable: true, rotationEligible: true, usageStatus: 'ok', usage: null}, extra);
const st = {activeNumber: 1, settings: [], accounts: [row(1, {isActive: true, alias: 'work'}), row(2), row(3, {kind: 'api_key', alias: 'key', disabled: true})]};
ctx.state = st;
ctx.renderAccounts(st);
const rowOf = n => ids['accounts-body'].children.find(tr => tr.getAttribute('data-key') === 'claude:' + n);
const menu = n => rowOf(n).all().filter(e => e.getAttribute('role') === 'menuitem').map(e => e.textContent);
const aliasItems = n => menu(n).filter(t => /alias/i.test(t));
assert.deepEqual(aliasItems(1), ['Change alias…', 'Remove alias'], 'the live account with an alias');
assert.deepEqual(aliasItems(2), ['Set alias…'], 'an account without an alias');
assert.deepEqual(aliasItems(3), ['Change alias…', 'Remove alias'], 'a disabled API-key account with an alias');

(async () => {
  for (const n of [1, 3]) { rowOf(n).all().find(e => e.textContent === 'Remove alias').click(); }
  await new Promise(r => setTimeout(r, 0));
  assert.deepEqual(requests, [['POST', '/api/accounts/claude%3A1/alias', undefined], ['POST', '/api/accounts/claude%3A3/alias', undefined]]);
  console.log('alias: Remove alias beside Change alias… passed');
})().catch(e => { console.error(e); process.exitCode = 1; });
