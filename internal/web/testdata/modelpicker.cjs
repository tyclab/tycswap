'use strict';

// The Settings tab's autoswitch.model pick list (DESIGN A49): which boxes it
// shows and ticks for a saved value and the windows the accounts report, and
// what Save sends back. Runs the production block of app.js, nothing copied.
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

// Nothing saved: one unticked box per reported window.
assert.deepEqual(opts(null, ['Fable']), { all: false, options: [{ name: 'Fable', checked: false }] });
assert.deepEqual(opts('', ['Fable']), { all: false, options: [{ name: 'Fable', checked: false }] });
// A saved name ticks its box, matched without case, shown as reported.
assert.deepEqual(opts('fable', ['Fable']), { all: false, options: [{ name: 'Fable', checked: true }] });
// "all" ticks All alone; the named boxes keep their own state.
assert.deepEqual(opts('all', ['Fable', 'Opus']), { all: true, options: [{ name: 'Fable', checked: false }, { name: 'Opus', checked: false }] });
assert.deepEqual(opts('ALL, Fable', ['Fable']), { all: true, options: [{ name: 'Fable', checked: true }] });
// A saved name no account reports keeps a ticked box, so a save never drops it.
assert.deepEqual(opts('Fable, Opus', ['Fable']), { all: false, options: [{ name: 'Fable', checked: true }, { name: 'Opus', checked: true }] });
assert.deepEqual(opts('Opus', []), { all: false, options: [{ name: 'Opus', checked: true }] });
// Names split on commas only: a window is called "Fable 5".
assert.deepEqual(opts('Fable 5', ['Fable 5', 'Opus']), { all: false, options: [{ name: 'Fable 5', checked: true }, { name: 'Opus', checked: false }] });
// A name reported twice, or saved twice, is one box.
assert.deepEqual(opts('Fable, fable', ['Fable', 'Fable']), { all: false, options: [{ name: 'Fable', checked: true }] });
// Nothing reported and nothing saved: no boxes, the row keeps its text field.
assert.deepEqual(opts(null, []), { all: false, options: [] });
assert.deepEqual(opts('all', []), { all: true, options: [] });

// What Save sends: "all", the ticked names comma-joined, or null for a reset
// (the server refuses an empty value).
assert.equal(ctx.modelPickerValue(true, ['Fable']), 'all');
assert.equal(ctx.modelPickerValue(false, ['Fable']), 'Fable');
assert.equal(ctx.modelPickerValue(false, ['Fable 5', 'Opus']), 'Fable 5, Opus');
assert.equal(ctx.modelPickerValue(false, []), null);
console.log('model picker options and values passed');
