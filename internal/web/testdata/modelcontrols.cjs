'use strict';
// A small DOM for the production editor and settings renderer. Event bubbling,
// focus, node identity and disabled state are deliberate: these regressions
// cannot be covered by testing only the pure options/value helpers.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const js = fs.readFileSync(process.argv[2], 'utf8');
const html = fs.readFileSync(process.argv[3], 'utf8');
const document = {activeElement: null, hidden: false};
class Node {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.attrs = {}; this.events = {}; this.value = ''; this.className = ''; this.disabled = false; }
  appendChild(n) { if (n.parentNode) n.parentNode.removeChild(n); this.children.push(n); n.parentNode = this; return n; }
  removeChild(n) { this.children.splice(this.children.indexOf(n), 1); n.parentNode = null; }
  get firstChild() { return this.children[0] || null; }
  setAttribute(k,v) { this.attrs[k] = String(v); if (k === 'value') this.value = String(v); }
  getAttribute(k) { return this.attrs[k] || null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(k,fn) { (this.events[k] ||= []).push(fn); }
  emit(k) { for (let n=this;n;n=n.parentNode) for (const f of n.events[k] || []) f.call(n,{target:this,key:k,preventDefault(){}}); }
  click() { if (!this.disabled) this.emit('click'); }
  focus() { document.activeElement = this; }
  blur() { document.activeElement = document.body; }
  contains(n) { return this === n || this.children.some(c=>c.contains(n)); }
  matches(s) { return s.startsWith('.') ? this.className.split(' ').includes(s.slice(1)) : this.tagName === s.toUpperCase(); }
  closest(s) { return s.split(',').some(x=>this.matches(x.trim())) ? this : this.parentNode?.closest(s) || null; }
  querySelectorAll(s) { const parts=s.split(',').map(x=>x.trim()); return this.children.flatMap(c=>[...(parts.some(x=>c.matches(x))?[c]:[]),...c.querySelectorAll(s)]); }
  querySelector(s) { return this.querySelectorAll(s)[0] || null; }
}
document.createElement = tag=>new Node(tag);
document.createTextNode = text=>Object.assign(new Node('#text'),{textContent:text});
document.body = new Node('body'); document.activeElement=document.body;
const ids = {};
for (const id of ['settings-grid','settings-empty','settings-sub']) { ids[id]=new Node('div'); }
ids['settings-grid'].className='settings-grid';
const ctx = {document, console, Promise, setTimeout, Math, state:null, minimumStateSequence:0, renderSigs:{}, modelSettingRow:null,
  $:id=>ids[id], modelWindowNames:st=>st.models || [], toast(){}, showTokenStatus:false, CSRF:'test', NAME:'tycswap', chip:()=>new Node('span')};
function block(start,end) { const a=js.indexOf(start),b=js.indexOf(end,a); assert.ok(a>=0&&b>a,start); vm.runInNewContext(js.slice(a,b),ctx); }
block('  var UNSAFE_ATTR', '  function fmtDur');
block('  function api(', '  // run wraps');
block('  // ---- model picker ', '  // ---- end model picker');
block('  var SETTING_LABELS', '  // ---- guarded section rendering');
block('  function editingInside(', '  // sigOf:');
block('  function render(st)', '  // ---- actions');
const productionRender = ctx.render;
ctx.render = st=>{ ctx.state=st; };
const sv = {key:'autoswitch.model',value:'Fable',kind:'string',isDefault:false};
(async()=>{
  const box=ctx.modelPicker(sv,'test',['Fable','Opus']);
  const mode=box.querySelector('select'), names=box.querySelector('.model-names');
  const original=names.children[0];
  assert.equal(mode.value,'selected');
  mode.focus(); box.sync('all',['Fable','Opus','New']);
  assert.equal(mode.value,'selected','an open select keeps its value');
  assert.equal(names.children[0],original,'open controls keep their nodes');
  mode.blur(); box.sync('Fable',['Fable','Opus']);
  assert.equal(names.children[0],original,'unchanged options do not rebuild');
  const checks=names.querySelectorAll('input');
  checks[0].checked=false; checks[1].checked=true; checks[1].emit('change');
  box.sync('all',['New']);
  assert.equal(box.pickerValue(),'Opus','draft survives a new account window and blur');
  let resolveSave; const requests=[];
  ctx.fetch = (url, opts)=>{ requests.push([opts.method,url,opts.body]); return new Promise(resolve=>{ resolveSave=resolve; }); };
  const saving=ctx.saveModelEditor(box,box.pickerValue());
  assert.ok(mode.disabled); box.sync(null,['New']); assert.equal(box.pickerValue(),'Opus');
  ctx.fetch=(url,opts)=>Promise.resolve({ok:true,text:()=>Promise.resolve(JSON.stringify({sequence:12,settings:[]}))});
  resolveSave({ok:true,text:()=>Promise.resolve(JSON.stringify({ok:true,stateSequence:11}))});
  await saving;
  assert.equal(JSON.parse(requests[0][2]).value,'Opus');
  assert.equal(ctx.minimumStateSequence,11);
  assert.equal(box.modelPending,false); assert.equal(box.modelDirty,false); assert.equal(mode.disabled,false);
  document.hidden=true; ctx.state={sequence:12};
  productionRender({sequence:10}); assert.equal(ctx.state.sequence,12,'late pre-save state is rejected');
  productionRender({sequence:13}); assert.equal(ctx.state.sequence,13,'newer states still arrive');
  document.hidden=false;
  box.sync('Opus',['Fable','Opus']); mode.value='off'; mode.emit('change');
  ctx.fetch=(url,opts)=>{ requests.push([opts.method,url]); return Promise.resolve({ok:true,text:()=>Promise.resolve(JSON.stringify(opts.method==='GET'?{sequence:15}:{stateSequence:14}))}); };
  await ctx.saveModelEditor(box,box.pickerValue());
  assert.equal(requests[1][0],'DELETE','Off unsets, never POSTs an empty string');
  box.sync('Opus',['Opus']); mode.value='all'; mode.emit('change');
  ctx.fetch=()=>Promise.resolve({ok:false,status:409,text:()=>Promise.resolve('{"error":"engine rejected save"}')});
  await ctx.saveModelEditor(box,box.pickerValue());
  assert.equal(box.pickerValue(),'all'); assert.equal(box.modelDirty,true,'failed save keeps a retryable draft'); assert.equal(mode.disabled,false);
  const empty=ctx.modelPicker({...sv,value:null},'empty',[]);
  assert.equal(empty.pickerValue(),null); empty.querySelector('select').value='selected';
  empty.querySelector('input').value='Fable 5, Unknown';
  assert.equal(empty.pickerValue(),'Fable 5, Unknown','manual names work before accounts report windows');
  const unknown=ctx.modelPicker({...sv,value:'Unknown'},'unknown',['Fable']);
  assert.equal(unknown.pickerValue(),'Unknown','unreported saved names survive');
  ctx.renderSettings({settings:[sv],models:['Fable','Opus']});
  const row=ctx.modelSettingRow, editor=row.querySelector('.model-picker'), checkbox=editor.querySelector('input');
  checkbox.focus(); assert.equal(ctx.editingInside(ids['settings-grid']),true,'settings checkbox is editing');
  checkbox.checked=false; checkbox.emit('change'); checkbox.blur();
  ctx.renderSettings({settings:[sv],models:['Fable','Opus','New']});
  assert.equal(ctx.modelSettingRow,row,'unsaved settings model row survives repaint after blur');
  assert.equal(editor.pickerValue(),null);
  assert.ok(html.indexOf('id="auto-model-picker"') < html.indexOf('id="auto-body"'),'Auto editor lives outside repaintable body');
  assert.match(html,/<select id="model-limits-toggle"/);
  console.log('model controls: focus, drafts, pending requests, ordering, failures and empty/reset paths passed');
})().catch(e=>{ console.error(e); process.exitCode=1; });
