'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const js = fs.readFileSync(process.argv[2], 'utf8');
const now = Date.now();
class FixedDate extends Date { static now() { return now; } }
const ctx = {Date: FixedDate};
function block(start, end) {
  const a = js.indexOf(start), b = js.indexOf(end, a);
  assert.ok(a >= 0 && b > a);
  return js.slice(a, b);
}
vm.runInNewContext(block('  function pctNum(', '  function accountName(') +
  block('  function isClaude(', '  // rowKey ') +
  block('  // ---- shared derivations ', '  function countingNote('), ctx);
const reset = hours => new Date(now + hours * 3600000).toISOString();
function account(number, week, model, burst, hours=24) {
 return {number, rotationEligible:true, usageStatus:'ok', usage:{
  sevenDay:{pct:week,resetsAt:reset(hours)},fiveHour:{pct:burst,resetsAt:reset(3)},
  scoped:[{name:'Fable',pct:model,resetsAt:reset(hours)}]
 }};
}
function rank(accounts, model='all', strategy='best') {
 return ctx.rankCandidates({accounts,settings:[{key:'autoswitch.model',value:model},{key:'autoswitch.strategy',value:strategy}]}).ranked;
}
function order(...args) { return Array.from(rank(...args), x => x.number); }
// A complete tie alone falls back to slot number, in either strategy.
for (const strategy of ['best','soonest-reset']) {
 assert.deepEqual(order([account(1,40,30,60),account(2,40,30,10),account(3,40,20,70),account(4,30,50,70)],'all',strategy),['4','3','2','1']);
}
// Disabled model limits neither sort nor block accounts.
assert.deepEqual(order([account(1,40,100,10),account(2,40,0,60)],''),['1','2']);
assert.deepEqual(order([account(1,40,100,10),account(2,40,0,60)],'all'),['2','1']);
// A full weekly window is always below usable accounts. A soon reset leads
// the blocked group even if the other account has lower model/burst usage.
const fullSoon=account(3,100,70,70,2);
for (const strategy of ['best','soonest-reset']) {
 const rows=rank([account(1,100,0,0,48),account(2,98,80,70,48),fullSoon],'all',strategy);
 assert.deepEqual(Array.from(rows,x=>x.number),['2','3','1']);
 assert.equal(rows[1].soonReset,true);
 assert.equal(rows[1].tier,3); // never claim it is usable yet
}
// Another full, enabled model window with a later reset prevents the promise.
const blocked=account(1,100,100,10,2);
blocked.usage.scoped[0].resetsAt=reset(48);
assert.equal(rank([blocked])[0].soonReset,false);
assert.equal(rank([blocked],'')[0].soonReset,true);
for (const stamp of [null,'broken',reset(-1),reset(0),reset(5),reset(6)]) {
 const a=account(1,100,0,0); a.usage.sevenDay.resetsAt=stamp;
 assert.equal(rank([a])[0].soonReset,false);
}
// Preserve reset strategy and filter active/disabled/Codex accounts.
assert.deepEqual(order([account(1,10,0,0,48),account(2,30,0,0,12)],'all','soonest-reset'),['2','1']);
const hidden=[{...account(3,0,0,0),isActive:true},{...account(4,0,0,0),rotationEligible:false},{...account(5,0,0,0),provider:'codex'}];
assert.deepEqual(order([...hidden,account(2,20,0,0)]),['2']);
console.log('hierarchical ranking and soon-reset groups passed');

const justSoon=account(1,100,0,0,5-1/3600);
assert.equal(rank([justSoon])[0].soonReset,true);

ctx.el = (tag, attrs) => attrs;
vm.runInNewContext(block('  function usageFreshness(', '  function menuButton('), ctx);
assert.equal(ctx.usageFreshness({}), null);
const stamp = reset(-0.2);
const cached = ctx.usageFreshness({usageFetchedAt:stamp, usageRefresh:{error:'http-429',nextAt:reset(0.1)}});
assert.match(cached.text, /Usage checked .*refresh rate-limited.*next attempt/);
assert.equal(cached.class, 'chip chip-warn');
assert.match(ctx.usageFreshness({usageRefresh:{error:'network'}}).text, /refresh failed/);
assert.doesNotMatch(ctx.usageFreshness({usageFetchedAt:stamp,usageRefresh:{nextAt:reset(1)}}).text, /failed|limited/);

const missing=account(1,0,0,0);delete missing.usage.scoped;
assert.deepEqual(order([missing,account(2,40,20,20)],'all'),['2','1']);
assert.equal(rank([missing],'Fable')[0].label,'model usage missing: fable');
assert.deepEqual(order([missing,account(2,40,20,20)],''),['1','2']);
