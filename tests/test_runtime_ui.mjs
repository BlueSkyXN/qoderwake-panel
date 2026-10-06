import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const management=readFileSync(new URL('../panel/static/management.js',import.meta.url),'utf8');
const panel=readFileSync(new URL('../panel/static/panel.js',import.meta.url),'utf8');
const definitions=management.split('\n').filter(line=>line.startsWith('function ')).join('\n');
function runtimeContext(extra={}){return vm.createContext({card:(_,body)=>body,esc:value=>String(value??''),btn:(action,label,index,extra='')=>`<button data-action="${action}" ${extra}>${label}</button>`,...extra})}
const base={desired:{hotDeploy:false,embeddingDisabled:true},effectiveOnPanelRestart:{hotDeploy:false,embeddingDisabled:true},observed:[],pendingRestart:null,restartAllowed:false,note:'fixture'};

test('unknown runtime is not a restart requirement and disables restart',()=>{
  const context=runtimeContext();vm.runInContext(definitions,context);
  const html=context.runtimeCard(base);
  assert.match(html,/未知：不能判断是否需要重启/);
  assert.match(html,/data-action="applyRuntime" disabled/);
  assert.doesNotMatch(html,/已核实：与保存策略不同/);
  assert.match(html,/会话投影上行/);
  assert.match(html,/继承启动环境/);
});
test('verified applied and changed runtime are distinct',()=>{
  const context=runtimeContext();vm.runInContext(definitions,context);
  for(const pending of [false,true]){
    const html=context.runtimeCard({...base,pendingRestart:pending,restartAllowed:true,observed:[{hotDeploy:false,embeddingDisabled:true}]});
    assert.match(html,pending?/已核实：与保存策略不同/:/已核实：与保存策略一致/);
    assert.doesNotMatch(html,/data-action="applyRuntime" disabled/);
  }
});
test('save then apply rejects HTTP 200 with ok false without success toast',async()=>{
  const requested=[],messages=[],routes=[];
  const values={'hot-deploy':{checked:false},'disable-embedding':{checked:true},'session-uplink':{value:'off'},'execution-uplink':{value:'inherit'}};
  const context=runtimeContext({actions:{},E:id=>values[id],errors:{},confirmAction:async()=>true,toast:message=>messages.push(message),route:async path=>routes.push(path),api:async(path,body)=>{requested.push({path,body});return path==='runtime/policy'?{ok:true,message:'saved'}:{ok:false,message:'identity not verified'}}});
  vm.runInContext(definitions+'\n'+panel.split('\n').find(line=>line.startsWith('async function mutate('))+'\n'+management.split('\n').find(line=>line.startsWith('actions.applyRuntime=')),context);
  await assert.rejects(context.actions.applyRuntime(),/identity not verified/);
  assert.deepEqual(requested.map(row=>row.path),['runtime/policy','runtime/apply']);
  assert.equal(requested[0].body.sessionProjectionUplink,false);
  assert.equal(requested[0].body.remoteExecutionUplink,null);
  assert.deepEqual(messages,['saved']);
  assert.deepEqual(routes,[]);
});
test('cancelled runtime application has no side effects',async()=>{
  let called=0;
  const context=runtimeContext({actions:{},confirmAction:async()=>false,mutate:()=>called++});
  vm.runInContext(management.split('\n').find(line=>line.startsWith('actions.applyRuntime=')),context);
  await context.actions.applyRuntime();assert.equal(called,0);
});
test('network status never equates unverified with stopped',()=>{
  const context=vm.createContext({});
  vm.runInContext(panel.split('\n').find(line=>line.startsWith('function gatewayStatus(')),context);
  assert.equal(context.gatewayStatus({managed:false,healthy:false}),'未受管 / 未核实');
  assert.equal(context.gatewayStatus({managed:true,healthy:false}),'身份或健康未核实');
  assert.equal(context.gatewayStatus({managed:true,healthy:true}),'已核实健康');
});
test('hash navigation follows browser history without duplicate renders',async()=>{
  const calls=[];const context=vm.createContext({identity:{level:'admin'},view:'controls',location:{hash:'#net'},route:async path=>calls.push(path)});
  vm.runInContext(panel.split('\n').find(line=>line.startsWith('function onHashChange(')),context);
  await context.onHashChange();assert.deepEqual(calls,['net']);
  context.view='net';await context.onHashChange();assert.equal(calls.length,1);
  context.identity=null;context.location.hash='#models';await context.onHashChange();assert.equal(calls.length,1);
});
test('viewer deep link falls back to overview instead of leaving a blank page',async()=>{
  const context=vm.createContext({identity:{level:'viewer'},nav:[['overview','总览','简介'],['controls','增强管理','简介']],isAdmin:()=>false,toast:()=>{},clearTimeout:()=>{},poll:null,view:'controls',generation:0,location:{hash:'#controls'},E:()=>({textContent:''}),document:{querySelectorAll:()=>[]},put:()=>{},empty:()=>'',card:()=>'',esc:value=>value,renderers:{overview:async()=> 'overview'},afterRender:{}});
  vm.runInContext(panel.split('\n').find(line=>line.startsWith('async function route(')),context);
  await context.route('controls');assert.equal(context.view,'overview');assert.equal(context.location.hash,'overview');
});
test('usage partial scan is explicitly labelled',()=>{
  const context=vm.createContext({});
  vm.runInContext(panel.split('\n').find(line=>line.startsWith('function usageProgress(')),context);
  assert.match(context.usageProgress({complete:false,pendingFiles:2,bytesRead:1024}),/统计尚不完整/);
  assert.match(context.usageProgress({complete:true,bytesRead:0}),/当前可读日志已扫描/);
});
