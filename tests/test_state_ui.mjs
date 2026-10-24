import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const source=readFileSync(new URL('../panel/static/panel.js',import.meta.url),'utf8');
const definition=source.split('\n').find(line=>line.startsWith('async function loadState('));
for(const role of ['admin','viewer','maintainer','observer']){
  const admin=role==='admin';
  test(`${role} state loader requests only permitted routes`,async()=>{
    const requested=[],banners=[];
    const label={textContent:'',classList:{toggle(){}}};
    const context=vm.createContext({identity:{level:role,displayName:'Local maintainer'},isAdmin:()=>admin,api:async path=>{
      requested.push(path);
      if(path==='maintenance'){
        assert.equal(admin,true);
        return {mode:'maintenance'};
      }
      return {daemon:'running',whoami:null};
    },showMaintenance:value=>banners.push(value.mode),E:()=>label});
    vm.runInContext('let state=null;'+definition,context);
    const result=await context.loadState();
    assert.equal(result.daemon,'running');
    assert.deepEqual(requested,admin?['state','maintenance']:['state']);
    assert.deepEqual(banners,[admin?'maintenance':'normal']);
    assert.equal(label.textContent,'Local maintainer');
  });
}

test('collaborators see their work pages and legacy viewers keep their existing menu',()=>{
  const definitions=source.split('\n').filter(line=>line.startsWith('const isAdmin=')||line.startsWith('const canView=')||line.startsWith('const can=')).join('\n');
  for(const role of ['maintainer','observer','viewer']){
    const context=vm.createContext({identity:{level:role,userId:role==='viewer'?null:'person',capabilities:role==='maintainer'?['skill.write']:[]}});
    vm.runInContext(definitions+'\nglobalThis.viewAllowed=canView;globalThis.writeAllowed=can;',context);
    assert.equal(context.viewAllowed('skills'),role!=='viewer');
    assert.equal(context.viewAllowed('audit'),role!=='viewer');
    assert.equal(context.viewAllowed('users'),false);
    assert.equal(context.viewAllowed('net'),false);
    assert.equal(context.viewAllowed('usage'),role==='viewer');
    assert.equal(context.writeAllowed('skill.write'),role==='maintainer');
  }
});
