'use strict';
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const script=fs.readFileSync(process.argv[2],'utf8');
function page(saved, unavailable=false) {
 const attrs={},events={},changes={};
 const picker={value:'',addEventListener:(n,fn)=>changes[n]=fn};

 const document={documentElement:{setAttribute:(k,v)=>attrs[k]=v,removeAttribute:k=>delete attrs[k]},querySelector:()=>({content:'tycswap'}),getElementById:()=>picker,addEventListener:(n,fn)=>events[n]=fn};
 Object.defineProperty(document,'cookie',{get(){if(unavailable)throw Error();return saved?'tycswap_appearance='+saved:'';},set(value){if(unavailable)throw Error();saved=value.includes('Max-Age=0')?null:value.split(';')[0].split('=')[1];}});
 vm.runInNewContext(script,{document});
 return {attrs,picker,load:()=>events.DOMContentLoaded(),choose:v=>{picker.value=v;changes.change();},saved:()=>saved};
}
for(const mode of ['light','dark']) {
 const p=page(mode);assert.equal(p.attrs['data-theme'],mode);p.load();assert.equal(p.picker.value,mode);
 p.choose(mode==='light'?'dark':'light');assert.equal(p.saved(),p.picker.value);
 p.choose('auto');assert.equal(p.attrs['data-theme'],undefined);assert.equal(p.saved(),null);
 const reloaded=page(p.saved());reloaded.load();assert.equal(reloaded.picker.value,'auto');
}
for(const value of [null,'invalid']){const p=page(value);p.load();assert.equal(p.picker.value,'auto');assert.equal(p.attrs['data-theme'],undefined);}
const denied=page(null,true);denied.load();denied.choose('dark');assert.equal(denied.attrs['data-theme'],'dark');
console.log('appearance persistence, overrides, auto, reloads and unavailable cookies passed');
