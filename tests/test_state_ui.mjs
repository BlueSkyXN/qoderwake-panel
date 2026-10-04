import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const source=readFileSync(new URL('../panel/static/panel.js',import.meta.url),'utf8');
const definition=source.split('\n').find(line=>line.startsWith('async function loadState()'));
for(const admin of [false,true]){
  test(`${admin?'admin':'viewer'} state loader requests only permitted routes`,async()=>{
    const requested=[],banners=[];
    const context=vm.createContext({isAdmin:()=>admin,api:async path=>{
      requested.push(path);
      if(path==='maintenance'){
        assert.equal(admin,true);
        return {mode:'maintenance'};
      }
      return {daemon:'running',whoami:null};
    },showMaintenance:value=>banners.push(value.mode),E:()=>({textContent:'',classList:{toggle(){}}})});
    vm.runInContext('let state=null;'+definition,context);
    const result=await context.loadState();
    assert.equal(result.daemon,'running');
    assert.deepEqual(requested,admin?['state','maintenance']:['state']);
    assert.deepEqual(banners,[admin?'maintenance':'normal']);
  });
}
