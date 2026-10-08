'use strict';

// Execute the production model-picker block and assert its saved/reported window contract.

const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const js = fs.readFileSync(process.argv[2], 'utf8');
const start = js.indexOf('  // ---- model picker ');
const end = js.indexOf('  // ---- end model picker', start);
assert.ok(start >= 0 && end > start, 'the model picker block exists');
const ctx = {};
vm.runInNewContext(js.slice(start, end), ctx);
const opts = (saved, reported) => JSON.parse(JSON.stringify(ctx.modelPickerOptions(saved, reported)));

assert.deepEqual(opts(null, ['Fable']), { all: false, options: [{ name: 'Fable', checked: false }] });
assert.deepEqual(opts('', ['Fable']), { all: false, options: [{ name: 'Fable', checked: false }] });
assert.deepEqual(opts('fable', ['Fable']), { all: false, options: [{ name: 'Fable', checked: true }] });
assert.deepEqual(opts('all', ['Fable', 'Opus']), { all: true, options: [{ name: 'Fable', checked: false }, { name: 'Opus', checked: false }] });
assert.deepEqual(opts('ALL, Fable', ['Fable']), { all: true, options: [{ name: 'Fable', checked: true }] });
assert.deepEqual(opts('Fable, Opus', ['Fable']), { all: false, options: [{ name: 'Fable', checked: true }, { name: 'Opus', checked: true }] });
assert.deepEqual(opts('Opus', []), { all: false, options: [{ name: 'Opus', checked: true }] });
assert.deepEqual(opts('Fable 5', ['Fable 5', 'Opus']), { all: false, options: [{ name: 'Fable 5', checked: true }, { name: 'Opus', checked: false }] });
assert.deepEqual(opts('Fable, fable', ['Fable', 'Fable']), { all: false, options: [{ name: 'Fable', checked: true }] });
assert.deepEqual(opts(null, []), { all: false, options: [] });
assert.deepEqual(opts('all', []), { all: true, options: [] });

assert.equal(ctx.modelPickerValue(true, ['Fable']), 'all');
assert.equal(ctx.modelPickerValue(false, ['Fable']), 'Fable');
assert.equal(ctx.modelPickerValue(false, ['Fable 5', 'Opus']), 'Fable 5, Opus');
assert.equal(ctx.modelPickerValue(false, []), null);
console.log('model picker options and values passed');
