import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const source=readFileSync(new URL('../panel/static/panel.js',import.meta.url),'utf8');
const render=source.slice(source.indexOf('function memoryError('),source.indexOf('const renderers='));
const start=source.indexOf('memory(){',source.indexOf('const afterRender='));
const method=source.slice(start,source.indexOf('\nwakers(){',start)).replace(/,$/,'');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

function fixture(){
  const requests=[],select={value:'a',addEventListener(name,fn){this.change=fn}},box={innerHTML:'',isConnected:true};
  const context=vm.createContext({E:id=>id==='mem-waker'?select:box,generation:1,
    esc,empty:message=>`<p>${esc(message)}</p>`,card:(title,body)=>`<h2>${esc(title)}</h2>${body}`,errors:{},
    api:path=>new Promise((resolve,reject)=>requests.push({path,resolve,reject}))});
  vm.runInContext(render,context);
  const mount=()=>vm.runInContext(`({${method}}).memory()`,context);
  return {context,select,box,requests,mount};
}
const files=content=>({kind:'files',docs:[{path:'notes.md',content}]});

test('late response for the previous Waker does not replace selected memory',async()=>{
  const f=fixture();f.mount();f.select.value='b';const next=f.select.change();
  f.requests[1].resolve(files('B'));await next;
  f.requests[0].resolve(files('A'));await tick();
  assert.match(f.box.innerHTML,/>B</);assert.doesNotMatch(f.box.innerHTML,/>A</);
});
test('late error and returning to the same Waker cannot overwrite newer results',async()=>{
  const f=fixture();f.mount();f.select.value='b';f.select.change();f.select.value='a';const next=f.select.change();
  f.requests[2].resolve(files('fresh'));await next;
  f.requests[1].reject(new Error('old error'));f.requests[0].resolve(files('stale'));await tick();
  assert.match(f.box.innerHTML,/fresh/);assert.doesNotMatch(f.box.innerHTML,/old error|stale/);
});
test('navigation and logout discard a pending memory response',async()=>{
  for(const change of [f=>{f.context.generation++},f=>{f.box.isConnected=false}]){
    const f=fixture();f.mount();change(f);f.requests[0].resolve(files('private memory'));await tick();
    assert.doesNotMatch(f.box.innerHTML,/private memory/);
  }
});
test('formal memory failure remains visible alongside legacy files and all text is escaped',()=>{
  const f=fixture();const html=f.context.memoryContent({kind:'personal_avatar',status:'unavailable',
    error:'PERSONAL_AVATAR_DWS_LOGIN_REQUIRED',docs:[{path:'old.md',content:'<script>file</script>'}]});
  assert.match(html,/完成分身记忆设置/);assert.match(html,/工作区记忆文件/);
  assert.match(html,/&lt;script&gt;/);assert.doesNotMatch(html,/<script>/);
  const records=f.context.memoryContent({kind:'personal_avatar',status:'ready',docs:[],
    memories:[{title:'<b>title</b>',content:'<img src=x>',updatedAt:'now'}]});
  assert.match(records,/分身记忆/);assert.match(records,/&lt;img/);assert.doesNotMatch(records,/<img/);
});
test('large document preview labels truncation and preserves the other document',()=>{
  const f=fixture();const html=f.context.memoryContent({kind:'files',truncated:true,docs:[
    {path:'MEMORY.md',content:'索引正文'},
    {path:'sessions/large.md',content:'长文前部',truncated:true}]});
  assert.match(html,/索引正文/);assert.match(html,/长文前部/);assert.match(html,/前 200 KiB/);
});
test('large formal memory labels its preview without hiding other records',()=>{
  const f=fixture();const html=f.context.memoryContent({kind:'personal_avatar',status:'ready',truncated:true,
    memories:[{title:'短记忆',content:'完整正文'},
      {title:'长记忆',content:'<preview>',truncated:true}],docs:[]});
  assert.match(html,/完整正文/);assert.match(html,/&lt;preview&gt;/);
  assert.equal((html.match(/前 200 KiB/g)||[]).length,1);
  assert.doesNotMatch(html,/<preview>/);
});
