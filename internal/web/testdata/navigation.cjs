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
function page(initialHash) {
  const elements = {}, events = {};
  const window = { location: { hash: initialHash }, addEventListener: (name, fn) => { events[name] = fn; } };
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
  const context = {
    window,
    document: { querySelectorAll: () => tabs },
    history: { replaceState: (_, __, hash) => { window.location.hash = hash; } },
    $: id => elements[id] || null
  };
  vm.runInNewContext(routing, context);
  return {
    navigate(hash) { window.location.hash = hash; events.hashchange(); },
    clickTab(name) { tabs.find(t => t.attrs['data-tab'] === name).events.click(); },
    assertTab(name, hash) {
      assert.equal(window.location.hash, hash, 'preserve the navigation target');
      names.forEach(n => assert.equal(elements['panel-' + n].hidden, n !== name, n + ' visibility'));
      tabs.forEach(t => assert.equal(t.attrs['aria-selected'], String(t.attrs['data-tab'] === name)));
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
