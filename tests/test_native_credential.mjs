import assert from 'node:assert/strict';
import test from 'node:test';
import {createCipheriv,randomBytes} from 'node:crypto';
import {PREFIX,credentialIdentity,decryptCredential} from '../panel/native-credential.mjs';

function fixture(){
  const identity=credentialIdentity('/fixture/home/.auth'),key=randomBytes(32),iv=randomBytes(12),token='a'.repeat(64);
  const cipher=createCipheriv('aes-256-gcm',key,iv);cipher.setAAD(identity.aad);
  const encrypted=Buffer.concat([cipher.update(token+'\n'),cipher.final()]);
  return {identity,key:key.toString('hex'),token,raw:PREFIX+Buffer.concat([iv,cipher.getAuthTag(),encrypted]).toString('base64')};
}
test('version-one credential reads only with matching directory, service and key',()=>{
  const f=fixture();assert.equal(decryptCredential(f.raw,f.key,f.identity),f.token);
  for(const identity of [credentialIdentity('/different/.auth'),credentialIdentity('/fixture/home/.auth','token','different.auth.v1')]){
    assert.throws(()=>decryptCredential(f.raw,f.key,identity));
  }
  assert.throws(()=>decryptCredential(f.raw,'00'.repeat(32),f.identity));
});
test('unknown versions, tampered payload and unrelated filenames are rejected',()=>{
  const f=fixture();
  for(const raw of [f.raw.replace('v1:','v2:'),PREFIX+'invalid',f.raw.slice(0,-5)+'AAAA']){
    assert.throws(()=>decryptCredential(raw,f.key,f.identity));
  }
  assert.throws(()=>credentialIdentity('/fixture/home/.auth','user'));
});
