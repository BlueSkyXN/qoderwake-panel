import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const source=readFileSync(new URL('../panel/static/panel.js',import.meta.url),'utf8');
const definition=source.slice(source.indexOf('async function confirmAction('),source.indexOf('async function mutate('));
function fixture(){
  const handlers=new Map(),trigger={isConnected:true,focused:false,focus(){this.focused=true}};
  const dialog={open:false,returnValue:'',showModal(){this.open=true},close(value){this.open=false;this.returnValue=value},addEventListener(name,fn){handlers.set(name,fn)},removeEventListener(name){handlers.delete(name)}};
  const message={textContent:''};
  const context=vm.createContext({E:id=>id==='confirm-dialog'?dialog:message,document:{activeElement:trigger},dialogTrigger:trigger,setTimeout:fn=>fn()});
  vm.runInContext(definition,context);
  const emit=(name,value)=>{let prevented=false;handlers.get(name)?.({preventDefault(){prevented=true},submitter:{value}});return prevented};
  return {dialog,handlers,message,trigger,emit,confirm:context.confirmAction};
}

test('submit confirms without relying on a later close event',async()=>{
  const f=fixture(),result=f.confirm('save');
  assert.equal(f.dialog.open,true);
  assert.equal(f.emit('submit','ok'),true);
  assert.equal(await result,true);
  assert.equal(f.dialog.open,false);
  assert.equal(f.handlers.size,0);
  assert.equal(f.trigger.focused,true);
});
test('cancel, escape and an external close never reuse an earlier confirmation',async()=>{
  const f=fixture();
  const first=f.confirm('first');f.emit('submit','ok');assert.equal(await first,true);
  for(const [name,value] of [['submit','cancel'],['cancel',''],['close','']]){
    const result=f.confirm('next');assert.equal(f.dialog.returnValue,'');f.emit(name,value);
    assert.equal(await result,false);assert.equal(f.handlers.size,0);
  }
});
test('explicit Escape fallback cancels and restores focus',async()=>{
  const f=fixture(),result=f.confirm('escape');
  let prevented=false;f.handlers.get('keydown')({key:'Escape',preventDefault(){prevented=true}});
  assert.equal(prevented,true);assert.equal(await result,false);
  assert.equal(f.dialog.open,false);assert.equal(f.trigger.focused,true);assert.equal(f.handlers.size,0);
});
test('an open confirmation is not replaced by another action',async()=>{
  const f=fixture(),first=f.confirm('first');
  await assert.rejects(f.confirm('second'),/请先完成当前确认/);
  assert.equal(f.message.textContent,'first');
  f.emit('cancel','');assert.equal(await first,false);
});
