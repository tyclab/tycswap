'use strict';
// Runs the whole production app.js over a DOM built from the production
// index.html, feeds each state document through the EventSource the page
// opens and selects every tab. It prints one JSON line per state with what
// the page shows (best candidate, next-best rows, at-limit marks, chips) and
// the problems it found: an exception, a toast, an undefined/null/NaN text,
// an Auto tab without "Counting now" or a Settings grid missing rows.
// usage: node pagestates.cjs <app.js> <index.html> <cases.json>
//   cases.json: [{"name": "...", "state": {...}}]
const fs = require('node:fs');
const vm = require('node:vm');

const [appPath, htmlPath, casesPath] = process.argv.slice(2);
const js = fs.readFileSync(appPath, 'utf8');
const html = fs.readFileSync(htmlPath, 'utf8').replace(/\{\{\.Name\}\}/g, 'tycswap').replace(/\{\{\.DisplayName\}\}/g, 'tycswap');
const cases = JSON.parse(fs.readFileSync(casesPath, 'utf8'));

const VOID = new Set(['meta', 'link', 'img', 'input', 'br', 'hr']);
const REFLECT = ['hidden', 'disabled', 'open'];
function makeDom() {
  const doc = { hidden: false, activeElement: null, listeners: {} };
  class Text {
    constructor(t) { this.nodeType = 3; this._text = String(t); this.parentNode = null; }
    get textContent() { return this._text; }
    set textContent(v) { this._text = v == null ? '' : String(v); }
    cloneNode() { return new Text(this._text); }
    all() { return []; }
  }
  class El {
    constructor(tag) {
      this.nodeType = 1; this.tagName = String(tag).toUpperCase(); this.attrs = {}; this.children = []; this.parentNode = null;
      this.style = {}; this.events = {}; this.checked = false; this.selected = false; this.required = false; this.tabIndex = 0; this._value = undefined;
      const self = this;
      this.classList = {
        add(c) { if (!self.classList.contains(c)) { self.className = (self.className + ' ' + c).trim(); } },
        remove(c) { self.className = self.className.split(/\s+/).filter(x => x && x !== c).join(' '); },
        contains(c) { return self.className.split(/\s+/).includes(c); },
        toggle(c, on) { const want = on === undefined ? !self.classList.contains(c) : !!on; if (want) { self.classList.add(c); } else { self.classList.remove(c); } return want; },
      };
    }
    get id() { return this.attrs.id || ''; } set id(v) { this.attrs.id = String(v); }
    get className() { return this.attrs.class || ''; } set className(v) { this.attrs.class = String(v); }
    get title() { return this.attrs.title || ''; } set title(v) { this.attrs.title = String(v); }
    get min() { return this.attrs.min || ''; } set min(v) { this.attrs.min = String(v); }
    get max() { return this.attrs.max || ''; } set max(v) { this.attrs.max = String(v); }
    get value() {
      if (this.tagName === 'SELECT') { const o = this.all().filter(n => n.tagName === 'OPTION'); const s = o.find(n => n.selected) || o[0]; return s ? (s.attrs.value !== undefined ? s.attrs.value : s.textContent) : ''; }
      return this._value !== undefined ? this._value : (this.attrs.value || '');
    }
    set value(v) { this._value = String(v); }
    get isContentEditable() { return false; }
    get firstChild() { return this.children[0] || null; }
    get textContent() { return this.children.map(c => c.textContent).join(''); }
    set textContent(v) { this.children.forEach(c => { c.parentNode = null; }); this.children = []; const s = v == null ? '' : String(v); if (s !== '') { this.appendChild(new Text(s)); } }
    appendChild(n) { if (n.parentNode) { n.parentNode.removeChild(n); } this.children.push(n); n.parentNode = this; return n; }
    removeChild(n) { const i = this.children.indexOf(n); if (i < 0) { throw new Error('removeChild: not a child'); } this.children.splice(i, 1); n.parentNode = null; return n; }
    setAttribute(k, v) { this.attrs[k] = String(v); }
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; }
    removeAttribute(k) { delete this.attrs[k]; }
    addEventListener(k, fn) { (this.events[k] = this.events[k] || []).push(fn); }
    dispatch(type) {
      const ev = { type, target: this, key: '', preventDefault() {} };
      for (let n = this; n; n = n.parentNode) { (n.events[type] || []).forEach(f => f.call(n, ev)); }
      (doc.listeners[type] || []).forEach(f => f.call(doc, ev));
    }
    click() { if (!this.disabled) { this.dispatch('click'); } }
    focus() { doc.activeElement = this; }
    scrollIntoView() {}
    showModal() { this.open = true; }
    close() { this.open = false; }
    contains(n) { for (let x = n; x; x = x.parentNode) { if (x === this) { return true; } } return false; }
    all() { const out = []; (function walk(e) { e.children.forEach(c => { if (c.nodeType === 1) { out.push(c); walk(c); } }); })(this); return out; }
    matches(sel) { return parseSelectorList(sel).some(chain => matchChain(this, chain)); }
    closest(sel) { for (let n = this; n; n = n.parentNode) { if (n.nodeType === 1 && n.matches(sel)) { return n; } } return null; }
    querySelectorAll(sel) { return this.all().filter(n => n.matches(sel)); }
    querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
    cloneNode(deep) { const c = new El(this.tagName); c.attrs = Object.assign({}, this.attrs); if (deep) { this.children.forEach(k => c.appendChild(k.cloneNode(true))); } return c; }
  }
  REFLECT.forEach(p => Object.defineProperty(El.prototype, p, {
    get() { return Object.prototype.hasOwnProperty.call(this.attrs, p); },
    set(v) { if (v) { this.attrs[p] = ''; } else { delete this.attrs[p]; } },
  }));
  function parseCompound(s) {
    const c = { tag: null, id: null, classes: [], attrs: [] };
    const re = /^([a-zA-Z][a-zA-Z0-9-]*)|#([\w-]+)|\.([\w-]+)|\[([\w:-]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\]]*)))?\]/;
    let rest = s;
    while (rest) {
      const m = re.exec(rest);
      if (!m) { throw new Error('harness: unsupported selector ' + JSON.stringify(s)); }
      if (m[1]) { c.tag = m[1].toUpperCase(); } else if (m[2]) { c.id = m[2]; } else if (m[3]) { c.classes.push(m[3]); }
      else { const val = m[5] !== undefined ? m[5] : m[6] !== undefined ? m[6] : m[7]; c.attrs.push([m[4], val]); }
      rest = rest.slice(m[0].length);
    }
    return c;
  }
  function parseSelectorList(sel) { return sel.split(',').map(p => p.trim().split(/\s+/).map(parseCompound)); }
  function matchCompound(n, c) {
    if (c.tag && n.tagName !== c.tag) { return false; }
    if (c.id && n.id !== c.id) { return false; }
    if (!c.classes.every(k => n.classList.contains(k))) { return false; }
    return c.attrs.every(([k, v]) => { const a = n.getAttribute(k); return a !== null && (v === undefined || a === v); });
  }
  function matchChain(n, chain) {
    if (!matchCompound(n, chain[chain.length - 1])) { return false; }
    let i = chain.length - 2;
    for (let p = n.parentNode; p && i >= 0; p = p.parentNode) { if (p.nodeType === 1 && matchCompound(p, chain[i])) { i--; } }
    return i < 0;
  }
  const root = new El('html');
  const stack = [root];
  const re = /<!--[\s\S]*?-->|<!doctype[^>]*>|<\/([a-zA-Z][\w-]*)\s*>|<([a-zA-Z][\w-]*)((?:\s+[^\s"'<>\/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'<>]+))?)*)\s*\/?>|([^<]+)/gi;
  const decode = s => s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&').replace(/&quot;/g, '"');
  let m;
  while ((m = re.exec(html))) {
    if (m[1]) {
      const tag = m[1].toUpperCase();
      for (let i = stack.length - 1; i > 0; i--) { if (stack[i].tagName === tag) { stack.length = i; break; } }
    } else if (m[2]) {
      const e = new El(m[2]);
      const are = /([^\s"'<>\/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'<>]+)))?/g;
      let a;
      while ((a = are.exec(m[3] || ''))) { e.attrs[a[1]] = decode(a[2] !== undefined ? a[2] : a[3] !== undefined ? a[3] : a[4] !== undefined ? a[4] : ''); }
      if ('checked' in e.attrs) { e.checked = true; }
      if ('selected' in e.attrs) { e.selected = true; }
      stack[stack.length - 1].appendChild(e);
      if (!VOID.has(m[2].toLowerCase())) { stack.push(e); }
    } else if (m[4] && stack.length > 1) {
      stack[stack.length - 1].appendChild(new Text(decode(m[4])));
    }
  }
  doc.body = root.querySelector('body');
  doc.activeElement = doc.body;
  doc.createElement = tag => new El(tag);
  doc.createTextNode = t => new Text(t);
  doc.getElementById = id => root.all().find(n => n.id === id) || null;
  doc.querySelectorAll = sel => root.querySelectorAll(sel);
  doc.querySelector = sel => root.querySelector(sel);
  doc.addEventListener = (k, fn) => { (doc.listeners[k] = doc.listeners[k] || []).push(fn); };
  return doc;
}

function page(state) {
  const document = makeDom();
  const sources = [], errors = [];
  const location = { hash: '#csrf=' + 'a'.repeat(32), search: '', pathname: '/' };
  class EventSource {
    constructor(url) { this.url = url; this.readyState = 1; this.listeners = {}; sources.push(this); }
    addEventListener(k, fn) { this.listeners[k] = fn; }
    close() { this.readyState = 2; }
  }
  const store = {};
  const ctx = {
    document, console, Promise, Math, Date, JSON, Object, Array, String, Number, isNaN, isFinite, parseFloat, parseInt, encodeURIComponent, URLSearchParams, URL, Error,
    location, EventSource,
    sessionStorage: { setItem(k, v) { store[k] = v; }, getItem(k) { return store[k] || null; }, removeItem(k) { delete store[k]; } },
    history: { replaceState(_, __, target) { const i = String(target).indexOf('#'); location.hash = i >= 0 ? String(target).slice(i) : ''; } },
    setTimeout() { return 0; }, setInterval() { return 0; }, clearTimeout() {},
    fetch() { return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve('{"ok":true}') }); },
  };
  ctx.window = ctx; ctx.addEventListener = () => {};
  vm.createContext(ctx);
  try { vm.runInContext(js, ctx, { filename: 'app.js' }); } catch (e) { errors.push('script load: ' + (e && e.stack || e)); }
  const es = sources[sources.length - 1];
  if (!es || !es.listeners.state) { errors.push('page opened no EventSource'); return { document, errors }; }
  try { es.listeners.state({ data: JSON.stringify(state) }); } catch (e) { errors.push('state event: ' + (e && e.stack || e)); }
  return { document, errors };
}

const BAD = /\bundefined\b|\bnull\b|\bNaN\b/;
function audit(document, ids) {
  const problems = [];
  ids.forEach(id => {
    const n = document.getElementById(id);
    if (!n) { problems.push('missing #' + id); return; }
    [n].concat(n.all()).forEach(e => {
      const own = e.children.filter(c => c.nodeType === 3).map(c => c.textContent).join('');
      if (BAD.test(own)) { problems.push('#' + id + ' <' + e.tagName.toLowerCase() + ' class="' + e.className + '"> text ' + JSON.stringify(own)); }
      Object.keys(e.attrs).forEach(k => { if (k !== 'id' && k !== 'data-sig' && BAD.test(e.attrs[k])) { problems.push('#' + id + ' <' + e.tagName.toLowerCase() + '> @' + k + '=' + JSON.stringify(e.attrs[k])); } });
    });
  });
  return problems;
}

const text = n => n ? n.textContent.replace(/\s+/g, ' ').trim() : null;
function chipIn(d, id) {
  const n = d.getElementById(id);
  const c = n && n.querySelector('.chip-warn');
  return c ? { text: text(c), title: c.title } : null;
}

let failed = 0;
for (const c of cases) {
  const { document: d, errors } = page(c.state);
  const problems = errors.slice();
  const toasts = d.getElementById('toasts');
  if (toasts) { toasts.children.forEach(t => problems.push('toast: ' + t.textContent)); } else { problems.push('no #toasts'); }
  const tabs = d.querySelectorAll('[role="tab"]');
  const names = tabs.map(t => t.getAttribute('data-tab'));
  tabs.forEach(t => {
    try { t.click(); } catch (e) { problems.push('tab ' + t.getAttribute('data-tab') + ': ' + (e && e.stack || e)); }
    names.forEach(n => { const panel = d.getElementById('panel-' + n); if (!panel || panel.hidden !== (n !== t.getAttribute('data-tab'))) { problems.push('tab ' + t.getAttribute('data-tab') + ': panel ' + n + ' visibility wrong'); } });
  });
  problems.push(...audit(d, ['panel-dashboard', 'panel-auto', 'panel-sessions', 'panel-settings', 'hdr-acct', 'tablist', 'version']));
  if (c.state.auto && !text(d.getElementById('auto-counting')).startsWith('Counting now: ')) { problems.push('auto-counting not rendered: ' + JSON.stringify(text(d.getElementById('auto-counting')))); }
  const grid = d.getElementById('settings-grid');
  const rows = grid ? grid.querySelectorAll('.setting-row').length : -1;
  if (Array.isArray(c.state.settings) && rows !== c.state.settings.length) { problems.push('settings rows ' + rows + ' != ' + c.state.settings.length); }
  const tiles = {};
  d.getElementById('summary-tiles').querySelectorAll('.tile').forEach(t => {
    tiles[text(t.querySelector('.tile-label'))] = { value: text(t.querySelector('.tile-value')), sub: text(t.querySelector('.tile-sub')) };
  });
  const best = tiles['Best candidate'] && tiles['Best candidate'].value.startsWith('#') ? tiles['Best candidate'].value.slice(1) : null;
  const atLimit = tiles['At limit'] && tiles['At limit'].sub.startsWith('#') ? tiles['At limit'].sub.split(' ').map(x => x.slice(1)) : [];
  const nextBest = d.getElementById('nextbest-list').children.map(tr => {
    const meter = tr.querySelectorAll('.c-mini')[2].querySelector('.meter');
    return {
      number: text(tr.querySelector('.slot-chip')).slice(1),
      verdict: text(tr.querySelector('.c-verdict')),
      model: meter && meter.querySelector('.meter-label') ? text(meter.querySelector('.meter-label')) : null,
      counted: meter && meter.querySelector('.meter-label') ? !meter.classList.contains('not-counted') : null,
    };
  });
  if (problems.length) { failed++; }
  const mythosDetail = text(d.getElementById('hdr-acct').querySelector('.hdr-mythos-detail'));
  console.log(JSON.stringify({ name: c.name, mythosDetail, problems, best, atLimit, nextBest, chips: { nextBest: chipIn(d, 'nextbest-sub'), accounts: chipIn(d, 'accounts-sub') } }));
}
process.exitCode = failed ? 1 : 0;
