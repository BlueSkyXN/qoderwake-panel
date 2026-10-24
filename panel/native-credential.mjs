// macOS 1.1.13 的 os-credential-v1 读取适配。只读现有钥匙串，不创建或迁移秘密。
import {createHash,createDecipheriv} from 'node:crypto';
import {open,realpath} from 'node:fs/promises';
import {constants} from 'node:fs';
import {basename,dirname} from 'node:path';
import {pathToFileURL} from 'node:url';

export const PREFIX='os-credential-v1:';
export function credentialIdentity(directory,filename='token',service='qoderwake.auth.v1'){
  if(!/^[A-Za-z0-9_.-]{1,100}$/.test(service)||filename!=='token')throw new Error('credential_format_unsupported');
  const name=createHash('sha256').update(directory).digest('hex');
  return {service,name,aad:Buffer.from(`${service}:${name}:${filename}`)};
}
export function decryptCredential(raw,keyHex,identity){
  if(!raw.startsWith(PREFIX)||!/^[a-f0-9]{64}$/.test(keyHex))throw new Error('credential_format_unsupported');
  const encoded=raw.slice(PREFIX.length).trim(),payload=Buffer.from(encoded,'base64');
  if(!/^[A-Za-z0-9+/]+={0,2}$/.test(encoded)||payload.length<28||payload.length>4096||payload.toString('base64')!==encoded)throw new Error('credential_format_unsupported');
  const key=Buffer.from(keyHex,'hex');
  try{
    const decipher=createDecipheriv('aes-256-gcm',key,payload.subarray(0,12));
    decipher.setAAD(identity.aad);decipher.setAuthTag(payload.subarray(12,28));
    const plaintext=Buffer.concat([decipher.update(payload.subarray(28)),decipher.final()]);
    try{const token=plaintext.toString('utf8').trim();if(!/^[a-f0-9]{64}$/.test(token))throw new Error('credential_format_unsupported');return token}
    finally{plaintext.fill(0)}
  }finally{key.fill(0)}
}
async function readNativeToken(file,service){
  if(process.platform!=='darwin'||typeof globalThis.Bun?.secrets?.get!=='function')throw new Error('credential_runtime_unavailable');
  const handle=await open(file,constants.O_RDONLY|constants.O_NOFOLLOW);
  let raw;
  try{
    const stat=await handle.stat();
    if(!stat.isFile()||stat.uid!==process.getuid()||(stat.mode&0o077)||stat.size>4096)throw new Error('credential_file_unavailable');
    raw=(await handle.readFile('utf8')).trim();
  }finally{await handle.close()}
  if(!raw.startsWith(PREFIX))throw new Error('credential_format_unsupported');
  const identity=credentialIdentity(await realpath(dirname(file)),basename(file),service);
  let key;
  try{key=await Bun.secrets.get({service:identity.service,name:identity.name})}catch{throw new Error('credential_store_unavailable')}
  if(typeof key!=='string')throw new Error('credential_store_unavailable');
  return decryptCredential(raw,key,identity);
}
if(process.argv[1]&&import.meta.url===pathToFileURL(process.argv[1]).href){
  try{
    const token=await readNativeToken(process.argv[2],process.argv[3]||'qoderwake.auth.v1');
    process.stdout.write(JSON.stringify({token}));
  }catch(error){
    const allowed=['credential_runtime_unavailable','credential_file_unavailable','credential_format_unsupported','credential_store_unavailable'];
    process.stderr.write(allowed.includes(error?.message)?error.message:'credential_decryption_failed');
    process.exitCode=1;
  }
}
