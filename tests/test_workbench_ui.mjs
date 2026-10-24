import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const source=readFileSync(new URL('../panel/static/advanced.js',import.meta.url),'utf8');
function workspace(){
  const editor={value:'draft',readOnly:false,isConnected:true},box={};
  const ctx=vm.createContext({URLSearchParams,nav:[],renderers:{},actions:{},afterRender:{},E:id=>id==='skill-content'?editor:box,
    view:'skills',confirmAction:async()=>false,conflictDetails:(...values)=>{ctx.conflict=values},
    api:async()=>({content:'remote update'}),mutate:async()=>{const error=new Error('version conflict');error.code='version_conflict';throw error}});
  vm.runInContext(source+"\nloadedSkill={wid:'w1',sid:'s1',version:'v1',editable:true,content:'original'};",ctx);
  return {ctx,editor};
}
test('leaving a dirty skill respects cancel without discarding the draft',async()=>{
  const {ctx,editor}=workspace();
  assert.equal(ctx.skillDirty(),true);assert.equal(await ctx.skillCanLeave(),false);
  assert.equal(editor.value,'draft');
  editor.value='original';assert.equal(await ctx.skillCanLeave(),true);
});
test('skill conflict preserves draft and unlocks the editor',async()=>{
  const {ctx,editor}=workspace();
  await assert.rejects(ctx.actions.saveSkill(),/version conflict/);
  assert.equal(editor.value,'draft');assert.equal(editor.readOnly,false);
  assert.deepEqual(Array.from(ctx.conflict).slice(1),['original','draft','remote update']);
});
test('save carries the read version and locks edits until the request completes',async()=>{
  const {ctx,editor}=workspace();let request;
  ctx.mutate=async(path,body)=>{request={path,body};assert.equal(editor.readOnly,true);return null};
  await ctx.actions.saveSkill();
  assert.equal(request.path,'skill/update');assert.equal(request.body.baseVersionId,'v1');assert.equal(request.body.content,'draft');
  assert.equal(editor.readOnly,false);
});
