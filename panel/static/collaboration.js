'use strict';
nav.push(['users','维护者','账号、角色与管理范围','M8 11a4 4 0 100-8 4 4 0 000 8M2 21v-3a6 6 0 0112 0v3M16 4a4 4 0 010 8M18 15a5 5 0 013 5']);
nav.push(['audit','变更记录','查看技能和配置的维护记录','M5 3h12v18H5zM8 7h6M8 11h6M8 15h4']);
nav.push(['account','我的账号','修改密码与管理登录','M11 12a5 5 0 100-10 5 5 0 000 10M3 22v-2a8 8 0 0116 0v2']);
const roleNames={admin:'管理员',maintainer:'维护者',observer:'观察者',viewer:'只读访问'};
const legacyWakers=renderers.wakers,legacyOverview=renderers.overview;
renderers.overview=async()=>legacyOverview();
actions.openWakers=()=>route('wakers');actions.openSkills=()=>route('skills');actions.openAudit=()=>route('audit');
renderers.wakers=async()=>{
  if(isAdmin())return legacyWakers();
  await loadState();
  return card('机器人',state.wakers.length?table(['名称','默认模型','操作'],state.wakers.map((w,i)=>`<tr><td>${esc(w.name)}<br><span class="mono muted">${esc(w.id)}</span></td><td>${esc(w.preference)}</td><td>${btn('editWaker',can('waker.write')?'编辑':'查看',i)}</td></tr>`)):empty('尚未分配机器人，请联系管理员。'))+`<section class="card" id="waker-editor" hidden></section>`;
};
function conflictDetails(box,original,draft,current){
  box.querySelector('[data-conflict]')?.remove();
  const section=document.createElement('section');section.dataset.conflict='1';
  const format=value=>typeof value==='string'?value:JSON.stringify(value,null,2);
  section.innerHTML=`<h3>当前版本已变化</h3><p>你的修改保留在编辑框中。请对照当前版本后重新读取，再合并需要保留的内容。</p>${[['读取时的内容',original],['你的修改',draft],['当前内容',current]].map(([label,value])=>`<details><summary>${label}</summary><pre class="code">${esc(format(value))}</pre></details>`).join('')}`;
  box.append(section);
}
function auditActionLabel(action){return ({'/api/skill/update':'保存技能','/api/skill/rollback':'恢复技能版本','/api/waker/update':'修改 Waker 配置','/api/waker':'创建 Waker','/api/waker/delete':'删除 Waker','/api/provider':'保存 Provider','/api/provider/delete':'删除 Provider','/api/preference':'设置默认模型','/api/native/execute':'执行官方操作','/api/users/create':'新增维护者','/api/users/update':'修改维护者','/api/users/password':'重置账号密码','/api/account/password':'修改密码'})[action]||action}
renderers.audit=async()=>{
  const rows=await api('audit');
  return card('维护变更',rows.length?table(['时间 / 操作者','动作 / 资源','结果','版本'],rows.map(r=>`<tr><td>${esc(r.ts)}<br>${esc(r.displayName||r.principal)}</td><td>${esc(r.action)}<br>${esc(r.target)}</td><td>${r.ok?'完成':'未完成'} · ${r.status}</td><td class="mono">${esc(r.beforeRevision||'—')}<br>→ ${esc(r.afterRevision||'—')}</td></tr>`)):empty('暂无可查看的变更。'));
};
let managedUsers=[];
renderers.users=async()=>{
  await loadState();const data=await api('users');managedUsers=data.items;
  if(data.mode!=='accounts')return card('维护者账号',`<p>在宿主机运行 panel-admin.py init-admin 创建首管理员，完成后使用用户名和密码登录。</p>`);
  return card('维护者',table(['账号','角色','范围','状态','操作'],managedUsers.map((u,i)=>`<tr><td>${esc(u.displayName)}<br>${esc(u.username)}</td><td>${roleNames[u.role]}</td><td>${u.role==='admin'?'全部':u.wakers.map(esc).join('<br>')||'未分配'}</td><td>${u.enabled?'启用':'停用'}</td><td>${btn('editUser','编辑',i)}${btn('passwordUser','重置密码',i)}</td></tr>`))+btn('newUser','新增维护者'))+`<section id="user-editor" class="card" hidden></section>`;
};
actions.newUser=()=>userEditor(null);
actions.editUser=i=>userEditor(managedUsers[i]);
function userEditor(user){
  const box=E('user-editor');box.hidden=false;
  box.innerHTML=`<h2>${user?'编辑账号':'新增账号'}</h2><form id="user-form"><div class="form-grid"><div><label for="user-login">用户名</label><input id="user-login" maxlength="64" required pattern="[A-Za-z0-9][A-Za-z0-9_.-]*" value="${esc(user?.username||'')}"${user?' disabled':''}></div><div><label for="user-name">显示名称</label><input id="user-name" maxlength="80" required value="${esc(user?.displayName||'')}"></div><div><label for="user-role">角色</label><select id="user-role">${Object.entries(roleNames).filter(([r])=>r!=='viewer').map(([r,n])=>`<option value="${r}"${r===(user?.role||'maintainer')?' selected':''}>${n}</option>`).join('')}</select></div><div><label for="user-wakers">管理范围（可多选）</label><select id="user-wakers" multiple>${state.wakers.map(w=>`<option value="${esc(w.id)}"${user?.wakers.includes(w.id)?' selected':''}>${esc(w.name||w.id)}</option>`).join('')}</select></div>${user?'':`<div><label for="user-password">初始密码</label><input id="user-password" type="password" minlength="10" maxlength="1024" autocomplete="new-password" required></div>`}</div>${user?`<label class="check-row"><input id="user-enabled" type="checkbox"${user.enabled?' checked':''}>启用账号</label>`:''}<div class="form-actions"><button class="primary">保存</button></div></form>`;
  const scopes=()=>{E('user-wakers').disabled=E('user-role').value==='admin'};E('user-role').addEventListener('change',scopes);scopes();
  E('user-form').addEventListener('submit',async event=>{
    event.preventDefault();const button=event.target.querySelector('button');button.disabled=true;
    try{const body={displayName:E('user-name').value,role:E('user-role').value,wakers:[...E('user-wakers').selectedOptions].map(o=>o.value)};
      if(user)Object.assign(body,{id:user.id,revision:user.revision,enabled:E('user-enabled').checked});
      else Object.assign(body,{username:E('user-login').value,password:E('user-password').value});
      await mutate(user?'users/update':'users/create',body);if(user?.id===identity.userId){showLogin('权限已更新，请重新登录');return}await route('users');
    }catch(error){toast(error)}finally{if(button.isConnected)button.disabled=false}
  });box.scrollIntoView({block:'nearest'});
}
actions.passwordUser=i=>{
  const user=managedUsers[i],box=E('user-editor');box.hidden=false;
  box.innerHTML=`<h2>重置 ${esc(user.displayName)} 的密码</h2><form id="user-reset"><label for="reset-password">新密码</label><input id="reset-password" type="password" minlength="10" maxlength="1024" autocomplete="new-password" required><p>保存后，该账号已有登录会话将失效。</p><button class="primary">重置密码</button></form>`;
  E('user-reset').addEventListener('submit',async event=>{event.preventDefault();const button=event.target.querySelector('button');button.disabled=true;try{await mutate('users/password',{id:user.id,revision:user.revision,password:E('reset-password').value});if(user.id===identity.userId)showLogin('密码已更新，请重新登录');else await route('users')}catch(error){toast(error)}finally{if(button.isConnected)button.disabled=false}});
};
renderers.account=async()=>card('我的账号',`<p>${esc(identity.displayName)} · ${esc(identity.username)} · ${esc(roleNames[identity.level])}</p><form id="password-form"><label for="current-password">当前密码</label><input id="current-password" type="password" autocomplete="current-password" required><label for="next-password">新密码</label><input id="next-password" type="password" minlength="10" maxlength="1024" autocomplete="new-password" required><div class="form-actions"><button class="primary">修改密码并重新登录</button></div></form>`);
afterRender.account=()=>E('password-form').addEventListener('submit',async event=>{event.preventDefault();const button=event.target.querySelector('button');button.disabled=true;try{await mutate('account/password',{currentPassword:E('current-password').value,password:E('next-password').value});showLogin('密码已更新，请重新登录')}catch(error){toast(error)}finally{if(button.isConnected)button.disabled=false}});
