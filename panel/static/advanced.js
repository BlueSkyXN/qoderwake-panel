'use strict';
nav.push(['operations','运行管理','系统状态、自动化与 IM 配对审批','M4 4h14v16H4zM8 8h6M8 12h6M8 16h6']);
nav.push(['sessions','会话管理','分页筛选、产物与受控删除','M3 5h16v12H8l-5 4z']);
nav.push(['skills','技能编辑','正文版本与冲突保护','M5 3h12v18H5zM8 7h6M8 11h6']);
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
  box.innerHTML=`<h2>运行历史</h2>${rows.length?table(['开始','状态','会话'],rows.map(r=>`<tr><td>${esc(r.startedAt)}</td><td>${esc(r.status)}</td><td class="mono">${esc(r.sessionId)}</td></tr>`)):empty()}${pager('run',page,nextPage(data,page))}`;
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
    card('高级会话列表',(managedSessions.length?table(['标题 / 来源','状态','未读','操作'],managedSessions.map((s,i)=>`<tr><td>${esc(s.title||s.session_id)}<br><span class="mono">${esc(s.origin||'')}</span></td><td>${esc(s.session_status||s.connection_status)}</td><td>${s.unread?'是':'否'}</td><td class="actions">${btn('sessionArtifacts','产物',i)}${btn('sessionRead','标已读',i)}${btn('sessionDelete','删除',i,'class="danger"')}</td></tr>`)):empty())+pager('session',sessionOffset/pageSize+1,!!data.has_more))+`<section id="artifact-list" class="card" hidden></section>`;
};
afterRender.sessions=()=>{
  E('session-waker').addEventListener('change',async()=>{managedWaker=E('session-waker').value;sessionOffset=0;await route('sessions')});
  E('session-origin').addEventListener('change',async()=>{managedOrigin=E('session-origin').value;sessionOffset=0;await route('sessions')});
};

renderers.skills=async()=>{
  await loadState();loadedSkill=null;skillRequest++;
  if(!state.wakers.some(w=>w.id===skillWaker))skillWaker=state.wakers[0]?.id||'';
  skillRows=skillWaker?await api('skills?'+query({waker:skillWaker})):[];
  return card('Skill 正文编辑',`<p class="muted">直接选择已有技能。内置、固定版本和无正文的技能只读；保存携带读取时的版本，冲突不会静默覆盖。</p><div class="form-grid"><div><label for="skill-waker">机器人</label><select id="skill-waker">${options(state.wakers,skillWaker)}</select></div><div><label for="skill-id">技能</label><select id="skill-id">${skillRows.map(s=>`<option value="${esc(s.skillId)}">${esc(s.name||s.skillId)}${s.pinned||s.mutableByAgent!==true?'（只读）':''}</option>`).join('')}</select></div></div><div class="form-actions">${btn('loadSkill','读取 Skill',undefined,skillRows.length?'':'disabled')}</div>`)+`<section id="skill-editor" class="card" hidden></section>`;
};
afterRender.skills=()=>{
  E('skill-waker').addEventListener('change',async()=>{skillWaker=E('skill-waker').value;await route('skills')});
  E('skill-id').addEventListener('change',()=>{loadedSkill=null;skillRequest++;E('skill-editor').hidden=true;E('skill-editor').replaceChildren()});
};
actions.loadSkill=async()=>{
  const wid=E('skill-waker').value,sid=E('skill-id').value,g=generation,request=++skillRequest;
  const d=await api('skill/content?'+query({waker:wid,skill:sid}));
  let versions=[],versionError='';
  try{versions=await api('skill/versions?'+query({waker:wid,skill:sid}))}catch(error){versionError=error.message}
  if(g!==generation||view!=='skills'||request!==skillRequest)return;
  managedVersions=versions;loadedSkill={wid,sid,version:d.skill.currentVersionId,editable:d.editable===true};
  const box=E('skill-editor');box.hidden=false;
  box.innerHTML=`<h2>${esc(d.skill.name||sid)}</h2>${!d.contentAvailable?'<div class="notice">该技能未提供可读取的正文，不能编辑；不是面板服务故障。</div>':!d.editable?'<div class="notice">该技能为只读或没有可用版本基线。</div>':''}${d.contentAvailable?`<label for="skill-content">技能正文</label><textarea class="skill-content" id="skill-content" maxlength="60000"${d.editable?'':' readonly'}>${esc(d.content)}</textarea>`:''}<div class="form-actions">${btn('saveSkill','按版本保存',undefined,d.editable?'':'disabled')}</div>${versionError?`<p class="muted">版本列表：${esc(versionError)}</p>`:''}${versions.length?table(['版本','时间','操作'],versions.map((x,i)=>`<tr><td class="mono">${esc(x.versionId)}</td><td>${esc(x.createdAt||x.updatedAt)}</td><td>${btn('diffSkill','差异',i)}${btn('rollbackSkill','回滚',i,d.editable?'':'disabled')}</td></tr>`)):empty('暂无版本历史')}<label for="rollback-reason">回滚原因</label><input id="rollback-reason" maxlength="500" value="面板管理员回滚"${d.editable?'':' disabled'}><pre id="skill-diff" class="code" hidden></pre>`;
  bindActions();box.scrollIntoView({block:'nearest'});
};
actions.saveSkill=async()=>{
  if(!loadedSkill?.editable)return;
  const current={...loadedSkill};const result=await mutate('skill/update',{wakerId:current.wid,skillId:current.sid,content:E('skill-content').value,baseVersionId:current.version},'保存 Skill 正文？并发版本变化将返回冲突。');
  if(result&&view==='skills')await actions.loadSkill();
};
actions.diffSkill=async i=>{
  if(!loadedSkill)return;const current={...loadedSkill},g=generation;
  const d=await api('skill/diff?'+query({waker:current.wid,skill:current.sid,version:managedVersions[i].versionId}));
  if(g!==generation||loadedSkill?.sid!==current.sid)return;E('skill-diff').hidden=false;E('skill-diff').textContent=JSON.stringify(d,null,2);
};
actions.rollbackSkill=async i=>{
  if(!loadedSkill?.editable)return;const reason=E('rollback-reason').value.trim();if(!reason)throw new Error('请填写回滚原因');
  const current={...loadedSkill};if(await mutate('skill/rollback',{wakerId:current.wid,skillId:current.sid,versionId:managedVersions[i].versionId,reason},'回滚到所选 Skill 版本？'))await actions.loadSkill();
};
