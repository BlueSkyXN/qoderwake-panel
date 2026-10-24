import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
const source=readFileSync(new URL('../panel/static/native.js',import.meta.url),'utf8');
const context=vm.createContext({module:{exports:{}}});vm.runInContext(source,context);
const {nativeParameterValue,nativeStateLabel,nativeOptionActive,nativeSection,nativeMatches,nativeJobNote}=context.module.exports;
test('repeatable flags preserve order and support embedded newlines',()=>{
  assert.deepEqual(Array.from(nativeParameterValue({repeatable:true},'one\ntwo')),['one','two']);
  assert.deepEqual(Array.from(nativeParameterValue({repeatable:true},'["one\\ntwo","three"]')),['one\ntwo','three']);
  assert.equal(nativeParameterValue({},'line one\nline two'),'line one\nline two');
  assert.throws(()=>nativeParameterValue({repeatable:true},'[1]'));
});
test('unknown outcome is not reported as successful or safely cancelled',()=>{
  assert.equal(nativeStateLabel('unknown'),'结果待核对');assert.equal(nativeStateLabel('cancelled'),'已停止命令');
});
test('person admin keeps the complete legacy overview',async()=>{
  const collaboration=readFileSync(new URL('../panel/static/collaboration.js',import.meta.url),'utf8');
  const context=vm.createContext({nav:[],renderers:{overview:async()=>'<legacy-health-models-network>',wakers:()=>{}},identity:{userId:'owner'},isAdmin:()=>true,actions:{},afterRender:{}});
  vm.runInContext(collaboration,context);
  assert.equal(await context.renderers.overview(),'<legacy-health-models-network>');
});

test('guided MCP modes exclude incompatible parameters and retain shared version guards',()=>{
  for(const mode of ['fields','json','file','all']){
    for(const name of ['--waker-id','--expected-revision','--idempotency-key'])assert.equal(nativeOptionActive(name,mode,'http'),true);
  }
  assert.equal(nativeOptionActive('--json','json','http'),true);
  assert.equal(nativeOptionActive('--json-file','file','stdio'),true);
  assert.equal(nativeOptionActive('--command','json','stdio'),false);
  assert.equal(nativeOptionActive('--http-url','fields','stdio'),false);
  assert.equal(nativeOptionActive('--command','fields','http'),false);
  assert.equal(nativeOptionActive('--oauth-client-secret','fields','stdio'),false);
  assert.equal(nativeOptionActive('--oauth-client-secret','fields','sse'),true);
  assert.equal(nativeOptionActive('--env','all','http'),true);
});

test('every catalog option maps to a known presentation section',()=>{
  // 每个官方参数只有一个稳定分组；原始契约仍是字段来源。
  const reference=JSON.parse(readFileSync(new URL('../panel/config/cli-reference.json',import.meta.url),'utf8'));
  for(const node of reference.nodes){
    for(const match of node.help.matchAll(/^  (--[\w-]+)/gm)){
      const section=nativeSection({name:match[1]}, {id:node.path.join('.'),group:node.path[0]});
      assert.ok(['basic','connection','import','auth','version','advanced'].includes(section));
    }
  }
});

test('parameter collection preserves omission, explicit empty, false and repeated values',()=>{
  const definition=source.slice(source.indexOf('function nativeParameters(){'),source.indexOf('\nasync function nativeVersion'));
  const nodes={};
  const spec={id:'mcp.add',arguments:[],options:[
    {name:'--name',value:true},{name:'--description',value:true},
    {name:'--auto-inject-at-waker-sender-id',value:true},{name:'--header',value:true,repeatable:true},
    {name:'--command',value:true},{name:'--force',value:false}
  ]};
  const values=['omitted','','false','a: 1\nb: 2','incompatible-command',''];
  spec.options.forEach((field,i)=>{
    nodes['native-opt-'+i]={value:values[i],checked:false,closest:()=>({hidden:i===4})};
    nodes['native-opt-'+i+'-use']={checked:i!==0};
  });
  const ctx=vm.createContext({nativeSpec:spec,E:id=>nodes[id],nativeParameterValue});
  vm.runInContext(definition,ctx);
  const result=JSON.parse(JSON.stringify(ctx.nativeParameters()));
  assert.deepEqual(result.options,{'--description':'','--auto-inject-at-waker-sender-id':'false','--header':['a: 1','b: 2']});
  nodes['native-opt-5'].checked=true;
  assert.equal(ctx.nativeParameters().options['--force'],true);
});


test('operation search accepts displayed CLI commands and unordered Chinese terms',()=>{
  const spec={id:'mcp.add',path:['mcp','add'],groupName:'MCP 服务器',description:'add an MCP server'};
  for(const query of ['mcp.add','mcp add','新增 MCP','MCP 新增','  mcp   add  ',''])assert.equal(nativeMatches(spec,query),true);
  for(const query of ['mcp delete','不存在的操作'])assert.equal(nativeMatches(spec,query),false);
});
test('completed commands do not suggest following a nonexistent task',()=>{
  const job={outputAvailable:true,truncated:false,error:''};
  assert.equal(nativeJobNote({...job,status:'succeeded'}),'');
  assert.ok(nativeJobNote({...job,status:'failed'}));
  assert.ok(nativeJobNote({...job,status:'queued'}));
  assert.ok(nativeJobNote({...job,status:'running'}));
  assert.ok(nativeJobNote({...job,status:'unknown'}));
  assert.ok(nativeJobNote({...job,status:'succeeded',outputAvailable:false}));
});
