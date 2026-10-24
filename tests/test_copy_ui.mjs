import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const panel=readFileSync(new URL('../panel/static/panel.js',import.meta.url),'utf8');
const management=readFileSync(new URL('../panel/static/management.js',import.meta.url),'utf8');
const copy=vm.createContext({});
vm.runInContext(panel.slice(panel.indexOf('const errors='),panel.indexOf('async function api(')),copy);

test('known backend states have distinct display labels without leaking raw enums',()=>{
  for(const [kind,values] of Object.entries({maintenance:['normal','draining','maintenance','restarting'],patch:['original','patched','unsupported','unavailable'],check:['clear','warning','blocked','unknown'],channel:[true,false,'connected','disconnected']})){
    const labels=values.map(value=>copy.uiStatus(kind,value));
    assert.equal(new Set(labels).size,values.length);
    labels.forEach(label=>assert.match(label,/[\u3400-\u9fff]/));
  }
  assert.notEqual(copy.uiStatus('channel','future_wire_value'),'future_wire_value');
});

test('actionable errors and readonly reasons are presented separately from codes',()=>{
  for(const code of ['channel_secret_required','provider_requires_https','unsupported_binary_hash','stop_daemon_before_patch','experimental_plugin_writes_disabled']){
    assert.notEqual(copy.uiError(code),code);
    assert.match(copy.uiError(code),/[\u3400-\u9fff]/);
  }
  assert.notEqual(copy.providerReadOnlyReason('provider_protocol_unsupported'),'provider_protocol_unsupported');
  assert.doesNotMatch(copy.uiError('future_backend_error'),/future_backend_error/);
});

test('login help follows account mode and encrypted transport',async()=>{
  const definition=panel.split('\n').find(line=>line.startsWith('async function loadAuthMode('));
  for(const mode of ['accounts','legacy'])for(const protocol of ['http:','https:']){
    const nodes={};const ctx=vm.createContext({api:async()=>({mode}),location:{protocol},E:id=>nodes[id]??=( {})});
    vm.runInContext("let authMode='legacy';"+definition,ctx);await ctx.loadAuthMode();
    assert.equal(nodes['login-user-fields'].hidden,mode!=='accounts');
    assert.equal(nodes['login-username'].required,mode==='accounts');
    assert.equal(nodes['login-transport'].hidden,protocol==='https:');
    assert.match(nodes['login-help'].textContent,mode==='accounts'?/账号/:/令牌/);
  }
});

for(const enabled of [true,false])test(`plugin blocked explanation follows write capability: ${enabled}`,async()=>{
  const detail={innerHTML:'',scrollIntoView(){}};
  const ctx=vm.createContext({nav:[],renderers:{plugins:async()=>''},afterRender:{},actions:{},isAdmin:()=>true,loadState:async()=>{},
    state:{pluginWritesEnabled:enabled,wakers:[]},E:()=>detail,esc:String,options:()=>'',table:()=>'',empty:()=>'',card:(_,body)=>body,bindActions(){},
    btn:(action,label,index,extra='')=>`<button data-action="${action}" ${extra}>${label}</button>`,
    api:async path=>path.startsWith('plugins/catalog')?{items:[{id:'demo',name:'Demo',version:'1'}],total:1}:path.startsWith('plugins/detail')?{name:'Demo',version:'1'}:{items:[]}});
  vm.runInContext(management,ctx);await ctx.renderers.plugins();await ctx.actions.pluginDetail(0);
  assert.equal(detail.innerHTML.includes('data-plugin-write-blocked'),!enabled);
  assert.equal(/data-action="pluginInstall" disabled/.test(detail.innerHTML),!enabled);
});


test('resource failure stays distinct from an empty accessible workspace',async()=>{
  const start=panel.indexOf('async overview(){'),end=panel.indexOf('\nasync models()',start);
  for(const failed of [true,false]){
    const ctx=vm.createContext({identity:{level:'admin'},roleNames:{admin:'admin'},state:{daemon:failed?'unavailable':'running',error:failed?'backend_unavailable':null,wakers:[],models:[]},
      isAdmin:()=>true,can:()=>true,canView:()=>false,loadState:async()=>{},actions:{},esc:String,kpis:()=>'',empty:text=>text,uiError:()=>'',errorDetails:()=>''});
    vm.runInContext('const renderers={'+panel.slice(start,end)+'};globalThis.renderOverview=renderers.overview;',ctx);
    const html=await ctx.renderOverview();
    assert.match(html,new RegExp('data-resource-state="'+(failed?'unavailable':'empty')+'"'));
  }
});


test('error toast exposes diagnostic code only in details and clears it on success',()=>{
  const elements={};const ctx=vm.createContext({E:id=>elements[id]??={},clearTimeout(){},setTimeout(){return 1}});
  vm.runInContext(panel.split('\n').find(line=>line.startsWith('function toast(')),ctx);
  const error=new Error('此请求未完成');error.code='synthetic_error';ctx.toast(error);
  assert.equal(elements['toast-message'].textContent,error.message);
  assert.equal(elements['toast-detail'].hidden,false);
  assert.equal(elements['toast-code'].textContent,error.code);
  ctx.toast('saved');assert.equal(elements['toast-detail'].hidden,true);assert.equal(elements['toast-code'].textContent,'');
});
