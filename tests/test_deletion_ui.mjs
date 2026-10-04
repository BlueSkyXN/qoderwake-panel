import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const source=readFileSync(new URL('../panel/static/panel.js',import.meta.url),'utf8');
const start=source.indexOf('function deletionStorageKey(');
const end=source.indexOf('async function loadState()');
const definition=source.slice(start,end);

function fixture(preview){
  const handlers=new Map(),inputs=[];
  const execute={hidden:false,disabled:false};
  const checks={innerHTML:'',querySelectorAll(selector){return selector==='[data-warning]'?inputs:selector==='[data-warning]:checked'?inputs.filter(x=>x.checked):[]}};
  const dialog={open:false,returnValue:'',showModal(){this.open=true},close(value){this.open=false;this.returnValue=value},addEventListener(name,fn){handlers.set(name,fn)},removeEventListener(name){handlers.delete(name)},querySelectorAll:checks.querySelectorAll.bind(checks)};
  const elements={'deletion-dialog':dialog,'deletion-execute':execute,'deletion-title':{textContent:''},'deletion-summary':{textContent:''},'deletion-checks':checks};
  const calls=[],storage=new Map(),trigger={isConnected:true,focus(){this.focused=true}};
  const context=vm.createContext({
    E:id=>elements[id],esc:String,toast(){},document:{activeElement:trigger},dialogTrigger:trigger,setTimeout:fn=>fn(),
    api:async(path,body,headers)=>{calls.push({path,body,headers});return preview},
    mutate:async(path,body,text,headers)=>{calls.push({path,body,text,headers});return {ok:true}},
    idempotencyKey:()=> 'fixture-key',errors:{deletion_result_unknown:'unknown',deletion_operation_not_found:'not found',deletion_operation_unresolved:'unresolved'},
    sessionStorage:{getItem:key=>storage.get(key)??null,setItem:(key,value)=>storage.set(key,value),removeItem:key=>storage.delete(key)}
  });
  vm.runInContext(definition,context);
  const emit=(name,value)=>handlers.get(name)?.({preventDefault(){},submitter:{value}});
  return {context,calls,dialog,execute,checks,inputs,emit,elements,storage};
}

test('blocked preview shows checks and never offers execution',async()=>{
  const f=fixture({deletable:false,completeness:'unavailable',warningsRequired:[],checks:[{id:'groups',label:'Group',status:'unknown',completeness:'unavailable',required:true,refs:[],note:'unavailable'}]});
  const pending=f.context.deletionAction('waker','w1');
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(f.dialog.open,true);
  assert.equal(f.execute.hidden,true);
  assert.match(f.checks.innerHTML,/unknown/);
  f.emit('submit','cancel');
  assert.equal(await pending,null);
  assert.equal(f.calls.length,1);
});

test('stored unknown operation reconciles without sending another delete',async()=>{
  const f=fixture({status:'unknown',result:{ok:false,status:'unknown'}});
  f.context.saveDeletion('waker','w1',{idempotencyKey:'fixture-key'});
  let count=0;
  f.context.api=async(path,body)=>{f.calls.push({path,body});count++;return count===1?{status:'unknown'}:{status:'succeeded',reconciled:true}};
  const result=await f.context.deletionAction('waker','w1');
  assert.equal(result.status,'succeeded');
  assert.deepEqual(f.calls.map(call=>call.path),['deletion/status','deletion/reconcile']);
  assert.equal(f.calls.some(call=>call.path==='waker/delete'),false);
  assert.equal(f.context.storedDeletion('waker','w1'),null);
});

test('warning preview submits impact digest and idempotency key',async()=>{
  const preview={id:'preview-id',impactDigest:'a'.repeat(64),deletable:true,completeness:'partial',warningsRequired:['history'],checks:[{id:'history',label:'History',status:'warning',completeness:'complete',required:false,refs:['session-1'],note:'confirm'}]};
  const f=fixture(preview);
  const input={checked:false,dataset:{warning:'history'},addEventListener(name,fn){this.change=fn}};
  f.inputs.push(input);
  const pending=f.context.deletionAction('provider','test','b'.repeat(64));
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(f.execute.disabled,true);
  input.checked=true;input.change();
  assert.equal(f.execute.disabled,false);
  f.emit('submit','execute');
  assert.deepEqual(JSON.parse(JSON.stringify(await pending)),{ok:true});
  const call=f.calls[1];
  assert.equal(call.path,'provider/delete');
  assert.equal(call.body.preview,'preview-id');
  assert.equal(call.body.confirm,'/api/provider/delete');
  assert.deepEqual(JSON.parse(JSON.stringify(call.body.acknowledgedWarnings)),['history']);
  assert.equal(call.headers['Idempotency-Key'],'fixture-key');
});
