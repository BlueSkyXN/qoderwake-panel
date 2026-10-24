'use strict';
nav.push(['operations','运行管理','系统状态、自动化与 IM 配对审批','M4 4h14v16H4zM8 8h6M8 12h6M8 16h6']);
nav.push(['sessions','会话管理','分页筛选、产物与受控删除','M3 5h16v12H8l-5 4z']);
nav.push(['skills','技能编辑','正文与版本历史','M5 3h12v18H5zM8 7h6M8 11h6']);
const pageSize=20;
let managedSessions=[],managedVersions=[],managedWaker='',managedOrigin='',sessionOffset=0;
let automationPage=1,pairingOffset=0,runPage=1,runTrigger='',skillWaker='',skillRows=[],loadedSkill=null,skillRequest=0;
const query=values=>new URLSearchParams(values).toString();
function pager(prefix,page,hasMore){return `<div class="row pagination">${btn(prefix+'Prev','上一页',undefined,page<=1?'disabled':'')}<span>第 ${page} 页</span>${btn(prefix+'Next','下一页',undefined,hasMore?'':'disabled')}</div>`}
function nextPage(data,page){const p=data.pagination||{};return Number.isFinite(p.total)?page*Number(p.pageSize||pageSize)<p.total:(data.items||[]).length===pageSize}

renderers.operations=async()=>{
  await loadState();
  const [system,automations,pairings]=await Promise.all([api('system/status'),api('automations?page='+automationPage),api('pairings?'+query({limit:pageSize,offset:pairingOffset}))]);
  const triggers=automations.items||[],pending=pairings.items||[];
  actions.toggleAutomation=async i=>{const t=triggers[i];if(await mutate('automation/toggle',{id:t.triggerId||t.id,enabled:!t.enabled},`${t.enabled?'停用':'启用'}此自动化？`))await route('operations')};
  actions.runAutomation=async i=>{const t=triggers[i];if(await mutate('automation/run',{id:t.triggerId||t.id},'立即执行可能消耗模型额度、运行命令或访问网络。确认提交？'))await route('operations')};
  actions.deleteAutomation=async i=>{const t=triggers[i];if(await mutate('automation/delete',{id:t.triggerId||t.id},'永久删除此自动化及其定义？'))await route('operations')};
  actions.automationRuns=async i=>{runTrigger=triggers[i].triggerId||triggers[i].id;runPage=1;await loadRuns()};
  actions.automationPrev=async()=>{automationPage=Math.max(1,automationPage-1);await route('operations')};
  actions.automationNext=async()=>{automationPage++;await route('operations')};
  actions.pairingPrev=async()=>{pairingOffset=Math.max(0,pairingOffset-pageSize);await route('operations')};
  actions.pairingNext=async()=>{pairingOffset+=pageSize;await route('operations')};
  actions.approvePairing=async i=>{
    const p=pending[i];
    if(await mutate('pairing/approve',{pendingId:p.pendingId,pendingRevision:p.pendingRevision,pendingOffset:pairingOffset,channelId:p.channelId,senderId:p.senderId,senderStaffId:p.senderStaffId||'',conversationId:p.conversationId,bindingKey:p.bindingKey,conversationType:p.conversationType,conversationName:p.conversationName||'',senderName:p.displayName||p.subjectName||'',wakerId:E('pairing-waker-'+i).value},`授权 ${p.displayName||p.conversationName||p.senderId} 触发所选 Waker？命令和自定义模型权限保持关闭。`))await route('operations');
  };
  const restart=system.update?.restartRequired;
  return kpis([['运行版本',system.update?.runningVersion||system.version||'未知','daemon 报告'],['安装版本',system.update?.installedVersion||'未知',restart===true?'需要重启':restart===false?'无需重启':'重启需求未知'],['运行会话',system.activity?.runningSessions??'未知','当前工作负载'],['待审批 IM',pairings.total??pending.length,'默认最小权限']])+
    card('自动化',(triggers.length?table(['名称 / 模型','状态','操作'],triggers.map((t,i)=>`<tr><td>${esc(t.triggerName||t.triggerId||t.id)}<br><span class="muted">${esc(t.taskDescription||t.model||'')}</span></td><td>${t.enabled?'启用':'停用'}</td><td class="actions">${btn('automationRuns','历史',i)}${btn('toggleAutomation',t.enabled?'停用':'启用',i)}${btn('runAutomation','立即运行',i)}${btn('deleteAutomation','删除',i,'class="danger"')}</td></tr>`)):empty('本页没有自动化'))+pager('automation',automationPage,nextPage(automations,automationPage)))+
    `<section id="automation-runs" class="card" hidden></section>`+
    card('IM 配对审批',(pending.length?table(['请求','目标 Waker','操作'],pending.map((p,i)=>`<tr><td>${esc(p.displayName||p.conversationName||p.senderId)}<br><span class="mono">${esc(p.channelId)} · ${esc(p.conversationType)}</span><br><span class="fine">版本 ${esc(p.pendingRevision??'未知')} · 有效期 ${esc(p.remoteExpiresAt||'以 daemon 为准')}</span></td><td><select aria-label="${esc('配对目标 '+(p.displayName||p.pendingId))}" id="pairing-waker-${i}">${options(state.wakers)}</select></td><td>${btn('approvePairing','最小权限批准',i,state.wakers.length?'':'disabled')}</td></tr>`)):empty('本页无待审批请求'))+pager('pairing',pairingOffset/pageSize+1,pairingOffset+pending.length<Number(pairings.total||0)));
};
async function loadRuns(){
  const g=generation,id=runTrigger,page=runPage;
  const data=await api('automation/runs?'+query({id,page}));
  if(g!==generation||view!=='operations'||id!==runTrigger||page!==runPage)return;
  const rows=data.items||[],box=E('automation-runs');box.hidden=false;
  box.innerHTML=`<h2>运行历史</h2>${rows.length?table(['开始','状态','会话'],rows.map(r=>`<tr><td>${esc(r.startedAt)}</td><td>${uiStatus('activity',r.status)}</td><td class="mono">${esc(r.sessionId)}</td></tr>`)):empty()}${pager('run',page,nextPage(data,page))}`;
  bindActions();box.scrollIntoView({block:'nearest'});
}
actions.runPrev=async()=>{runPage=Math.max(1,runPage-1);await loadRuns()};
actions.runNext=async()=>{runPage++;await loadRuns()};

renderers.sessions=async()=>{
  await loadState();
  if(!state.wakers.some(w=>w.id===managedWaker)){managedWaker=state.wakers[0]?.id||'';sessionOffset=0}
  const data=managedWaker?await api('sessions/query?'+query({waker:managedWaker,offset:sessionOffset,limit:pageSize,origin:managedOrigin})):{items:[]};
  managedSessions=data.items||[];
  actions.sessionArtifacts=async i=>{
    const sid=managedSessions[i].session_id,g=generation,d=await api('session/artifacts?'+query({session:sid}));
    if(g!==generation||view!=='sessions')return;
    const rows=d.artifacts||[],box=E('artifact-list');box.hidden=false;
    box.innerHTML=`<h2>会话产物</h2><p class="fine muted">只下载本会话清单中的文件，最大 32 MiB；不在页面执行 HTML 或 SVG。</p>${rows.length?table(['标题','类型','相对路径',''],rows.map(a=>`<tr><td>${esc(a.title||a.id)}</td><td>${esc(a.type)}</td><td class="mono">${esc(a.relativePath||'')}</td><td><a download href="${esc('/api/session/artifact-file?'+query({session:sid,artifact:a.id}))}">下载</a></td></tr>`)):empty('没有产物')}`;
    box.scrollIntoView({block:'nearest'});
  };
  actions.sessionRead=async i=>{await mutate('session/read',{sessionId:managedSessions[i].session_id});await route('sessions')};
  actions.sessionDelete=async i=>{if(await mutate('session/delete',{sessionId:managedSessions[i].session_id,force:false},'删除此会话及其事件和产物？运行中的会话不会强制删除。'))await route('sessions')};
  actions.sessionPrev=async()=>{sessionOffset=Math.max(0,sessionOffset-pageSize);await route('sessions')};
  actions.sessionNext=async()=>{sessionOffset+=pageSize;await route('sessions')};
  return card('筛选',`<div class="row"><label for="session-waker">机器人</label><select id="session-waker">${options(state.wakers,managedWaker)}</select><label for="session-origin">来源</label><select id="session-origin"><option value=""${managedOrigin===''?' selected':''}>全部</option><option value="chat"${managedOrigin==='chat'?' selected':''}>Chat</option><option value="at_waker"${managedOrigin==='at_waker'?' selected':''}>@Waker</option></select></div>`)+
    card('高级会话列表',(managedSessions.length?table(['标题 / 来源','状态','未读','操作'],managedSessions.map((s,i)=>`<tr><td>${esc(s.title||s.session_id)}<br><span class="mono">${esc(s.origin||'')}</span></td><td>${uiStatus('activity',s.session_status||s.connection_status)}</td><td>${s.unread?'是':'否'}</td><td class="actions">${btn('sessionArtifacts','产物',i)}${btn('sessionRead','标已读',i)}${btn('sessionDelete','删除',i,'class="danger"')}</td></tr>`)):empty())+pager('session',sessionOffset/pageSize+1,!!data.has_more))+`<section id="artifact-list" class="card" hidden></section>`;
};
afterRender.sessions=()=>{
  E('session-waker').addEventListener('change',async()=>{managedWaker=E('session-waker').value;sessionOffset=0;await route('sessions')});
  E('session-origin').addEventListener('change',async()=>{managedOrigin=E('session-origin').value;sessionOffset=0;await route('sessions')});
};

let selectedSkill='',skillPane='content';
function skillDirty(){return !!(loadedSkill?.editable&&E('skill-content')&&E('skill-content').value!==loadedSkill.content)}
async function skillCanLeave(){return !skillDirty()||await confirmAction('技能正文有未保存的修改。放弃这份草稿并离开？')}
function skillEditorState(){
  const dirty=skillDirty(),content=E('skill-content');
  if(E('skill-save-status'))E('skill-save-status').textContent=loadedSkill?.editable?(dirty?'未保存修改':'已保存'):'只读';
  if(E('skill-save'))E('skill-save').disabled=!loadedSkill?.editable||!dirty;
  if(E('skill-content-meta'))E('skill-content-meta').textContent=content?`${content.value.split('\n').length} 行 · ${content.value.length.toLocaleString()} 字符`:'';
}
function skillShowPane(name){
  skillPane=name;
  ['content','history'].forEach(id=>{E('skill-pane-'+id).hidden=id!==name;E('skill-tab-'+id).setAttribute('aria-pressed',String(id===name))});
}
function skillList(){
  const q=(E('skill-search')?.value||'').toLowerCase();
  const rows=skillRows.filter(s=>[s.name,s.skillId].join(' ').toLowerCase().includes(q));
  E('skill-items').innerHTML=rows.length?rows.map(s=>`<button class="skill-item" type="button" data-skill="${esc(s.skillId)}" aria-current="${String(s.skillId===selectedSkill)}"><strong>${esc(s.name||s.skillId)}</strong><small>${s.pinned||s.mutableByAgent!==true?'只读技能':can('skill.write')?'可编辑':'可查看'}${s.currentVersionId?' · '+esc(s.currentVersionId):''}</small></button>`).join(''):empty('没有匹配的技能');
  E('skill-items').querySelectorAll('[data-skill]').forEach(button=>button.addEventListener('click',async()=>{
    if(button.dataset.skill===selectedSkill&&loadedSkill)return;
    try{if(!await skillCanLeave())return;selectedSkill=button.dataset.skill;E('skill-id').value=selectedSkill;skillList();await actions.loadSkill()}catch(error){toast(error)}
  }));
}
renderers.skills=async()=>{
  await loadState();loadedSkill=null;skillRequest++;
  if(!state.wakers.some(w=>w.id===skillWaker))skillWaker=state.wakers[0]?.id||'';
  skillRows=skillWaker?await api('skills?'+query({waker:skillWaker})):[];
  if(!skillRows.some(s=>s.skillId===selectedSkill))selectedSkill=(skillRows.find(s=>s.mutableByAgent===true&&!s.pinned)||skillRows[0])?.skillId||'';
  return `<div class="skill-workspace"><aside class="skill-browser" aria-label="技能资源"><label for="skill-waker">Waker</label><select id="skill-waker">${options(state.wakers,skillWaker)}</select><label class="sr-only" for="skill-search">查找技能</label><input id="skill-search" type="search" placeholder="查找技能…"><div class="section-caption"><span>技能</span><span>${skillRows.length} 项</span></div><div id="skill-items" class="skill-items"></div><select id="skill-id" hidden aria-label="当前技能">${skillRows.map(s=>`<option value="${esc(s.skillId)}"${s.skillId===selectedSkill?' selected':''}>${esc(s.name||s.skillId)}</option>`).join('')}</select>${btn('reloadSkill','重新读取',undefined,`class="reload-skill"${skillRows.length?'':' disabled'}`)}</aside><section id="skill-editor" class="skill-stage" aria-label="技能工作区">${empty(skillRows.length?'正在读取技能…':state.error?'暂时无法读取技能。':state.wakers.length?'当前 Waker 没有技能。':isAdmin()?'请先在机器人页面创建 Waker。':'尚未分配 Waker，请联系管理员。')}</section></div>`;
};
afterRender.skills=async()=>{
  beforeNavigate=skillCanLeave;unsavedChanges=skillDirty;skillList();
  E('skill-search').addEventListener('input',skillList);
  E('skill-waker').addEventListener('change',async()=>{
    const next=E('skill-waker').value;
    if(!await skillCanLeave()){E('skill-waker').value=skillWaker;return}
    beforeNavigate=null;skillWaker=next;selectedSkill='';await route('skills');
  });
  if(selectedSkill)await actions.loadSkill();
};
actions.reloadSkill=async()=>{if(!skillDirty()||await confirmAction('重新读取将替换未保存的草稿。继续？'))await actions.loadSkill()};
actions.loadSkill=async()=>{
  const wid=E('skill-waker').value,sid=E('skill-id').value,g=generation,request=++skillRequest;
  loadedSkill=null;E('skill-editor').innerHTML=empty('正在读取技能…');
  const d=await api('skill/content?'+query({waker:wid,skill:sid}));
  let versions=[],versionError='';
  try{versions=await api('skill/versions?'+query({waker:wid,skill:sid}))}catch(error){versionError=error.message}
  if(g!==generation||view!=='skills'||request!==skillRequest)return;
  managedVersions=versions;const row=skillRows.find(s=>s.skillId===sid);if(row)row.currentVersionId=d.skill.currentVersionId;loadedSkill={wid,sid,version:d.skill.currentVersionId,editable:d.editable===true,content:d.content};
  const box=E('skill-editor'),wakerName=state.wakers.find(w=>w.id===wid)?.name||wid;
  box.innerHTML=`<header class="skill-editor-header"><div><h2>${esc(d.skill.name||sid)}</h2><p class="skill-context">${esc(wakerName)} · <span class="mono">${esc(d.skill.currentVersionId||'无版本')}</span></p></div><div class="skill-save"><span id="skill-save-status"></span>${btn('saveSkill','保存修改',undefined,'id="skill-save" class="primary" disabled')}</div></header>
    <div class="skill-tabs" role="group" aria-label="技能视图"><button id="skill-tab-content" type="button" aria-pressed="true" aria-controls="skill-pane-content">正文</button><button id="skill-tab-history" type="button" aria-pressed="false" aria-controls="skill-pane-history">版本与差异 <span class="muted">${versions.length}</span></button></div>
    <section id="skill-pane-content" class="skill-pane">${!d.contentAvailable?'<div class="notice">该技能未提供可读取的正文。</div>':!d.editable?`<div class="notice">${d.readOnlyReason==='permission_denied'?'当前账号可查看正文与版本。':'该技能为只读，或没有可用的版本基线。'}</div>`:''}${d.contentAvailable?`<label class="sr-only" for="skill-content">技能正文</label><textarea class="skill-content" id="skill-content" maxlength="60000" spellcheck="false"${d.editable?'':' readonly'}>${esc(d.content)}</textarea>`:''}</section>
    <section id="skill-pane-history" class="skill-pane" hidden><p class="skill-version-note">当前版本 <span class="mono">${esc(d.skill.currentVersionId||'未提供')}</span> · 选择历史版本查看差异。</p>${versionError?`<p class="error-text">${esc(versionError)}</p>`:''}${versions.length?table(['版本','时间','操作'],versions.map((x,i)=>`<tr><td class="mono">${esc(x.versionId)}${x.versionId===d.skill.currentVersionId?' <span class="badge">当前</span>':''}</td><td>${esc(x.createdAt||x.updatedAt)}</td><td class="actions">${btn('diffSkill','查看差异',i)}${btn('rollbackSkill','恢复此版本',i,`class="danger"${d.editable?'':' disabled'}`)}</td></tr>`)):empty('暂无版本历史')}<div id="skill-rollback" class="skill-rollback" hidden><h3 id="skill-rollback-title"></h3><label for="rollback-reason">恢复原因</label><input id="rollback-reason" maxlength="500" placeholder="说明这次恢复的原因"><div class="form-actions">${btn('confirmRollback','确认恢复',undefined,'class="danger"')}${btn('cancelRollback','取消')}</div></div><pre id="skill-diff" class="code" hidden tabindex="0"></pre></section>
    <footer class="skill-editor-foot"><span>${d.editable?'Markdown · ⌘ S 保存':'Markdown · 只读'}</span><span id="skill-content-meta"></span></footer>`;
  E('skill-content')?.addEventListener('input',skillEditorState);
  E('skill-content')?.addEventListener('keydown',event=>{if((event.metaKey||event.ctrlKey)&&event.key.toLowerCase()==='s'){event.preventDefault();if(!E('skill-save').disabled)E('skill-save').click()}});
  ['content','history'].forEach(id=>E('skill-tab-'+id).addEventListener('click',()=>skillShowPane(id)));
  skillShowPane(skillPane);skillEditorState();skillList();bindActions();
};
actions.saveSkill=async()=>{
  if(!loadedSkill?.editable)return;
  const current={...loadedSkill},editor=E('skill-content');let result;editor.readOnly=true;
  try{result=await mutate('skill/update',{wakerId:current.wid,skillId:current.sid,content:E('skill-content').value,baseVersionId:current.version},'保存当前技能正文？')}
  catch(error){if(error.code==='version_conflict'){const latest=await api('skill/content?'+query({waker:current.wid,skill:current.sid}));if(view==='skills'&&loadedSkill?.wid===current.wid&&loadedSkill?.sid===current.sid)conflictDetails(E('skill-editor'),current.content,E('skill-content').value,latest.content)}throw error}finally{if(editor.isConnected)editor.readOnly=!current.editable}
  if(result&&view==='skills'&&loadedSkill?.wid===current.wid&&loadedSkill?.sid===current.sid)await actions.loadSkill();
};
actions.diffSkill=async i=>{
  if(!loadedSkill)return;const current={...loadedSkill},g=generation;
  const d=await api('skill/diff?'+query({waker:current.wid,skill:current.sid,version:managedVersions[i].versionId}));
  if(g!==generation||loadedSkill?.wid!==current.wid||loadedSkill?.sid!==current.sid)return;
  E('skill-diff').hidden=false;E('skill-diff').textContent=JSON.stringify(d,null,2);
};
let rollbackVersion='';
actions.rollbackSkill=i=>{
  if(!loadedSkill?.editable)return;rollbackVersion=managedVersions[i].versionId;
  E('skill-rollback-title').textContent='恢复到 '+rollbackVersion;E('skill-rollback').hidden=false;E('rollback-reason').value='';E('rollback-reason').focus();
};
actions.cancelRollback=()=>{rollbackVersion='';E('skill-rollback').hidden=true};
actions.confirmRollback=async()=>{
  if(!loadedSkill?.editable||!rollbackVersion)return;
  const reason=E('rollback-reason').value.trim();if(!reason)throw new Error('请填写恢复原因');
  const current={...loadedSkill};
  if(await mutate('skill/rollback',{wakerId:current.wid,skillId:current.sid,versionId:rollbackVersion,baseVersionId:current.version,reason},`恢复到版本 ${rollbackVersion}？${skillDirty()?'当前未保存的草稿将被替换。':''}`))await actions.loadSkill();
};
