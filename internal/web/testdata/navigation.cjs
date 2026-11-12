'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const js = fs.readFileSync(process.argv[2], 'utf8');
const html = fs.readFileSync(process.argv[3], 'utf8');
const start = js.indexOf('  // ---- tabs ');
const end = js.indexOf('  // segmented controls', start);
assert.ok(start >= 0 && end > start, 'tab routing block exists');
const routing = js.slice(start, end);
const names = [...html.matchAll(/data-tab="([^"]+)"/g)].map(m => m[1]);
const sectionIDs = [...html.matchAll(/<article class="card guide-section" id="([^"]+)"/g)].map(m => m[1]);
const anchors = [...html.matchAll(/href="(#g-[^"]+)"/g)].map(m => m[1]);
assert.ok(names.includes('guide') && anchors.length > 0);

// Only DOM operations used by routing are needed; the production JavaScript
// handles selection, hash changes and scrolling, with no copied route logic.
function page(initialHash, search = '') {
  const elements = {}, events = {};
  const window = { location: { hash: initialHash, search, pathname: '/' }, addEventListener: (name, fn) => { events[name] = fn; } };
  const tabs = names.map(name => {
    elements['panel-' + name] = { hidden: true };
    return {
      attrs: { 'data-tab': name }, events: {},
      getAttribute(key) { return this.attrs[key]; },
      setAttribute(key, value) { this.attrs[key] = value; },
      addEventListener(name, fn) { this.events[name] = fn; },
      focus() {}
    };
  });
  let scrolled = null;
  sectionIDs.forEach(id => {
    elements[id] = {
      classList: { contains: name => name === 'guide-section' },
      scrollIntoView() {
        assert.equal(elements['panel-guide'].hidden, false, 'show Guide before scrolling');
        scrolled = id;
      }
    };
  });
  elements.tablist = { addEventListener() {} };
  let unfolds = 0, focused = 0, updateScrolls = 0;
  elements['updates-card'] = {
    hidden: true, folded: true,
    classList: { contains: name => name === 'folded' && elements['updates-card'].folded },
    scrollIntoView() { updateScrolls++; }
  };
  elements['updates-ok'] = { scrollIntoView() { updateScrolls++; } };
  elements['updates-title'] = { focus() { focused++; } };
  const context = {
    window, URLSearchParams, state: null, Promise,
    foldCard(id, folded) { unfolds++; elements[id].folded = folded; },
    document: { querySelectorAll: () => tabs },
    history: { replaceState: (_, __, target) => { const url = new URL(target, 'http://localhost/'); window.location.hash = url.hash; window.location.search = url.search; } },
    $: id => elements[id] || null
  };
  vm.createContext(context);
  const showStart = js.indexOf('  function showUpdates() {');
  const showEnd = js.indexOf('  var ACTIONS = {', showStart);
  vm.runInContext(js.slice(showStart, showEnd), context);
  vm.runInContext(routing, context);
  return {
    navigate(hash) { window.location.hash = hash; events.hashchange(); },
    clickTab(name) { tabs.find(t => t.attrs['data-tab'] === name).events.click(); },
    assertTab(name, hash) {
      assert.equal(window.location.hash, hash, 'preserve the navigation target');
      names.forEach(n => assert.equal(elements['panel-' + n].hidden, n !== name, n + ' visibility'));
      tabs.forEach(t => assert.equal(t.attrs['aria-selected'], String(t.attrs['data-tab'] === name)));
    },
    receiveUpdates(available) {
      context.state = context.st = { updates: { available } };
      elements['updates-card'].hidden = !available;
      const tailStart = js.indexOf('    applyFolds(st);');
      const tailEnd = js.indexOf('    tickCountdowns();', tailStart);
      context.applyFolds = () => {};
      vm.runInContext(js.slice(tailStart, tailEnd), context);
    },
    assertUpdates(available, scrolls = 1) {
      this.assertTab('dashboard', '#updates');
      assert.equal(unfolds, 1, 'unfold once');
      assert.equal(updateScrolls, scrolls, 'scroll once after state arrives');
      assert.equal(focused, available ? 1 : 0, 'only focus visible update title');
    },
    assertSection(hash) {
      this.assertTab('guide', hash);
      assert.equal(scrolled, hash.slice(1), 'scroll to the requested Guide section');
    }
  };
}

for (const anchor of anchors) {
  const p = page('#guide');
  p.assertTab('guide', '#guide');
  p.navigate(anchor);
  p.assertSection(anchor);
  p.clickTab('settings');
  p.assertTab('settings', '#settings');
  p.navigate(anchor); // browser Back returns to the Guide section
  p.assertSection(anchor);
  page(anchor).assertSection(anchor); // reload or direct fragment link
}
for (const name of names) {
  page('#' + name).assertTab(name, '#' + name);
}
page('#g-missing').assertTab('dashboard', '#dashboard');
page('').assertTab('dashboard', '#dashboard');
console.log('Guide contents, direct links, history and tab navigation passed');

for (const available of [false, true]) {
  for (const entry of [['#updates', ''], ['', '?show=updates']]) {
    const p = page(...entry);
    p.assertTab('dashboard', '#updates');
    p.receiveUpdates(available);
    p.assertUpdates(available);
    p.receiveUpdates(available);
    p.assertUpdates(available);
    p.clickTab('settings');
    p.receiveUpdates(available);
    p.assertTab('settings', '#settings');
  }
}
const csrfStart = js.indexOf('  var CSRF = (function () {');
const csrfEnd = js.indexOf('  // The command name', csrfStart);
const token = 'a'.repeat(32);
const stored = {};
const launch = {
  window: { location: { hash: '#csrf=' + token, pathname: '/', search: '?show=updates' } },
  sessionStorage: { setItem(k, v) { stored[k] = v; }, getItem(k) { return stored[k]; } },
  history: { replaceState(_, __, target) { assert.equal(target, '/?show=updates'); } }
};
vm.runInNewContext(js.slice(csrfStart, csrfEnd), launch);
assert.equal(launch.CSRF, token);
assert.equal(stored.csrf, token);
console.log('Updates launch, deferred navigation and repaint focus passed');

const changedBeforeLoad = page('#updates');
changedBeforeLoad.clickTab('settings');
changedBeforeLoad.receiveUpdates(true);
changedBeforeLoad.assertTab('settings', '#settings');
