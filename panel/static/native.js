'use strict';
function nativeParameterValue(field,value){
  if(field.variadic||field.repeatable){
    let rows;
    try{rows=JSON.parse(value)}catch{rows=value.split('\n').filter(v=>v!=='')}
    if(!Array.isArray(rows)||!rows.length||rows.some(v=>typeof v!=='string'))throw new Error('多值参数请逐行填写，或使用 JSON 字符串数组');
    return rows;
  }
  return value;
}
function nativeStateLabel(status){return ({queued:'等待执行',running:'执行中',succeeded:'命令成功退出',failed:'命令退出失败',unknown:'结果待核对',cancelled:'已停止命令'})[status]||status}
const nativeVerbs={list:'查看列表',get:'读取',read:'读取',show:'查看',detail:'查看详情',info:'查看信息',inspect:'检查',status:'查看状态',create:'新建',add:'新增',update:'更新',set:'设置',delete:'删除',remove:'移除',clear:'清空',reset:'重置',enable:'启用',disable:'停用',start:'启动',stop:'停止',restart:'重启',run:'运行',cancel:'取消',check:'检查',validate:'校验',search:'搜索',import:'导入',export:'导出',upload:'上传',download:'下载',install:'安装',uninstall:'卸载',authorize:'授权',login:'登录',logout:'退出登录',bind:'绑定',connect:'连接',restore:'恢复',rollback:'恢复版本',diff:'比较版本',versions:'版本历史',save:'保存',manage:'管理',rename:'重命名',clone:'复制',approve:'批准',reject:'拒绝',confirm:'确认',refresh:'刷新',reload:'重新加载',sync:'同步',watch:'跟随状态',wait:'等待结果',events:'事件',history:'历史记录',usage:'用量',stats:'统计',summary:'摘要',config:'配置',settings:'设置',messages:'消息',send:'发送',invoke:'调用',readiness:'就绪检查',version:'版本信息',whoami:'当前身份',progress:'进度',artifacts:'产物',pending:'待处理项',runs:'运行记录',tasks:'任务',task:'任务',activity:'活动',activities:'活动记录',context:'上下文',sources:'来源',publish:'发布',unregister:'取消注册',register:'注册',upgrade:'升级',downgrade:'降级',migrate:'迁移',init:'初始化',setup:'配置向导',bootstrap:'初始化',diagnose:'诊断',mutate:'修改',resolve:'解决',respond:'回复',toggle:'切换状态',hide:'隐藏',ignore:'忽略',skip:'跳过',retire:'停用',interrupt:'中断',claim:'领取',submit:'提交',build:'构建',instantiate:'创建实例',activate:'激活',onboard:'接入',relink:'重新关联',schedule:'调度',snapshot:'快照',recall:'检索记忆',dream:'整理记忆',market:'市场目录',featured:'精选目录',name:'名称',description:'描述',identity:'身份',profile:'资料',plan:'套餐',api:'API',network:'网络',mcp:'MCP',skill:'技能',persona:'角色设定',calendar:'日程',missions:'任务清单',trajectory:'执行轨迹',evidence:'证据',types:'类型',shared:'共享内容',around:'附近记录',range:'区间记录',stream:'持续输出',portal:'控制台入口',feedback:'反馈报告',coreCapabilities:'核心能力',deliveryCommitments:'交付约定',workStyles:'工作风格',bible:'规程',avatar:'分身',cli_status:'CLI 状态','add-waker':'添加 Waker','api-endpoint':'API 地址','bootstrap-status':'初始化状态','builtin-tools':'内置工具','capabilities-refresh':'刷新能力','cli-status':'CLI 状态','config-summary':'配置摘要','delete-by-target':'按目标删除','delete-impact':'删除影响','file-guard':'文件保护','filter-options':'筛选选项','from-file':'从文件导入','generate-code':'生成代码','install-and-authorize':'安装并授权','manual-add':'手动添加','mission-workroom':'任务工作区','model-security':'模型权限','qr-connect':'二维码连接','qr-poll':'查询扫码状态','qr-start':'开始扫码','refresh-tools':'刷新工具','run-get':'读取运行记录','run-now':'立即运行','run-retry':'重试运行','run-terminate':'终止运行','self-evolution':'自演进设置','set-meta':'修改元信息','set-model':'设置模型','test-pull':'测试拉取','toggle-targets':'切换目标','tool-catalog':'工具目录','tool-guard':'工具保护','tool-guard-rules':'工具保护规则',confirmations:'待确认项',operation:'操作',upsert:'保存或更新'};
function nativeTitle(spec){
  if(spec.id==='mcp.auth.start')return '授权 MCP 服务器';
  if(spec.path.length===1)return spec.groupName;
  const leaf=spec.path.at(-1),verb=nativeVerbs[leaf];
  return `${spec.groupName} · ${verb||leaf}`;
}
function nativeOptionActive(name,mode,transport){
  if(mode==='all')return true;
  if(['--waker-id','--expected-revision','--idempotency-key'].includes(name))return true;
  if(mode==='json')return name==='--json';
  if(mode==='file')return name==='--json-file';
  if(['--json','--json-file'].includes(name))return false;
  if(['--command','--args','--env'].includes(name))return transport==='stdio';
  if(name==='--http-url'||name==='--headers'||name==='--header'||name.startsWith('--oauth-')||name==='--auto-inject-at-waker-sender-id')return ['http','sse'].includes(transport);
  return true;
}
function nativeMatches(spec,search){const terms=String(search||'').trim().toLowerCase().split(/\s+/).filter(Boolean),text=[spec.id,(spec.path||[]).join(' '),spec.description,spec.groupName,nativeTitle(spec)].join(' ').toLowerCase();return terms.every(term=>text.includes(term))}
function nativeJobNote(job){if(!job.outputAvailable)return '当前输出不可用，可根据操作记录核对资源状态。';if(job.error)return uiError(job.error);if(job.truncated)return '当前保留最近 2 MiB 输出。';if(job.status==='queued')return '离开页面后仍会执行。';if(job.status==='running')return '离开页面后命令继续运行。';if(job.status==='failed')return '请查看上方输出中的失败原因。';if(job.status==='unknown')return '执行结果尚未确认，请先核对目标资源状态。';return ''}
function nativeSection(field,spec){
  const name=field.name;
  if(['--expected-revision','--base-version-id','--idempotency-key'].includes(name))return 'version';
  if(name.startsWith('--oauth-')||['--headers','--header','--auto-inject-at-waker-sender-id'].includes(name))return 'auth';
  if(spec.id==='mcp.add'&&['--json','--json-file'].includes(name))return 'import';
  if(spec.group==='mcp'&&['--command','--args','--env','--http-url'].includes(name))return 'connection';
  if(['--format','--force','--yes','--dry-run','--apply','--no-open'].includes(name)||name==='--json'&&!field.value)return 'advanced';
  return 'basic';
}
if(typeof module!=='undefined')module.exports={nativeParameterValue,nativeStateLabel,nativeTitle,nativeOptionActive,nativeSection,nativeMatches,nativeJobNote};
else {
nav.push(['native','官方操作','项目、MCP、技能、配置、协作及完整官方管理功能','M3 3h16v18H3zM7 7h8M7 12h8M7 17h5']);
let nativeCatalog=[],nativeSpec=null,nativeGroup='',nativeSearch='',nativeSelected='',nativeOwner='',nativeGuard={},nativeJob='',nativeOffset=0,nativeOutput='',nativeFiles=[],nativeTimer=null,nativeRequest=null,nativeFormDirty=false;
const nativeLabels={'--waker-id':'Waker','--skill-id':'技能 ID','--project-id':'项目 ID','--session-id':'Session ID','--conversation-id':'Conversation ID','--name':'名称','--description':'说明','--content':'正文','--content-file':'正文文件','--file':'文件','--json-file':'JSON 文件','--json':'JSON','--format':'输出格式','--enabled':'启用','--path':'路径','--out':'输出文件','--output':'输出路径','--to':'目标版本','--from':'起始版本','--reason':'原因','--base-version-id':'读取时的版本','--expected-revision':'读取时的配置版本','--idempotency-key':'上游幂等键','--yes':'确认应用','--apply':'应用修改','--dry-run':'只预览','--force':'强制操作','--follow':'持续跟随','--no-open':'不打开宿主机浏览器','--command':'启动命令','--args':'启动参数','--env':'环境变量','--http-url':'服务器地址','--transport':'连接方式','--headers':'请求头 JSON','--header':'请求头','--mcp-id':'MCP ID','--oauth-client-metadata-url':'客户端元数据地址','--oauth-client-id':'客户端 ID','--oauth-client-secret':'客户端密钥','--oauth-token-auth-method':'令牌认证方式','--oauth-scope':'授权范围','--oauth-resource-metadata-url':'资源元数据地址','--auto-inject-at-waker-sender-id':'注入发送者 ID'};
const nativeContextLabels={'QODERWAKE_CONVERSATION_ID':'Conversation ID','QODERWAKE_CONVERSATION_KIND':'Conversation kind','QODERWAKE_RUN_ID':'Run ID','QODERWAKE_RUN_TOKEN':'当前 Run 凭据','QODERWAKE_PARTICIPANT_ID':'Participant ID','QODER_AGENT_ID':'Agent ID','QODER_SESSION_ID':'worker Session ID','QODERWAKE_SESSION_ID':'daemon Session ID','QODERWAKE_CLOUD_CONVERSATION':'Cloud Conversation 上下文'};
const nativeFileField=f=>f.value&&(/file|path|zip|archive|output|out$/.test(f.name)||/file|path|zip/.test(f.placeholder||''));
renderers.native=async()=>{
  clearTimeout(nativeTimer);
  await loadState();
  const owner=identity.userId||identity.csrf;
  if(owner!==nativeOwner){nativeOwner=owner;nativeSearch='';nativeGroup='';nativeSpec=null;nativeSelected='';nativeFiles=[];nativeRequest=null;nativeJob='';nativeOutput='';nativeFormDirty=false}
  const [catalog,jobs,files]=await Promise.all([api('native/catalog'),api('native/jobs'),isAdmin()?api('native/files'):Promise.resolve({items:[]})]);nativeCatalog=catalog.items;nativeFiles=files.items;
  const groups=[...new Map(nativeCatalog.map(x=>[x.group,x.groupName])).entries()];
  return `<div class="native-workspace"><aside class="native-catalog" aria-label="官方操作目录"><div class="native-filters"><div><label for="native-search">查找操作</label><input id="native-search" type="search" value="${esc(nativeSearch)}" placeholder="名称、命令或功能…"></div><div><label for="native-group">功能分组</label><select id="native-group"><option value="">全部分组</option>${groups.map(([id,name])=>`<option value="${id}"${id===nativeGroup?' selected':''}>${esc(name)}</option>`).join('')}</select></div></div><p id="native-count"></p><div id="native-list" class="native-list"></div></aside><div class="native-stage"><div id="native-welcome" class="native-welcome"><h2>选择一项操作</h2><p>查找官方功能，在这里填写参数、执行操作并查看结果。</p><p class="mono">${nativeCatalog.length} 项可访问操作 · 按当前角色与资源范围提供</p></div><section id="native-editor" class="card" hidden></section><section id="native-job" class="card" hidden></section><section id="native-files" class="card" hidden></section></div></div>`+
    `<details class="native-history"><summary>我的操作记录 · ${jobs.items.length}</summary>${jobs.items.length?table(['操作','时间','结果',''],jobs.items.map(j=>`<tr><td class="mono">${esc(j.operation)}</td><td>${esc(new Date(j.created*1000).toLocaleString())}</td><td>${esc(nativeStateLabel(j.status))}</td><td><button type="button" data-native-job="${j.id}">查看结果</button></td></tr>`)):empty('尚未执行操作。')}</details>`;
};
function nativeList(){
  const q=nativeSearch.toLowerCase(),rows=nativeCatalog.filter(x=>(!nativeGroup||x.group===nativeGroup)&&nativeMatches(x,q));
  E('native-count').textContent=`${rows.length} 项匹配 · 共 ${nativeCatalog.length} 项`;
  const listScroll=E('native-list').scrollTop;
  E('native-list').innerHTML=rows.length?rows.map(x=>`<button type="button" class="native-operation" data-native-id="${esc(x.id)}" aria-current="${String(x.id===nativeSelected)}"><span class="native-operation-title"><strong>${esc(nativeTitle(x))}</strong><span class="operation-kind${['delete','remove','reset','clear','uninstall'].includes(x.path.at(-1))?' danger':''}">${x.effect==='read'?'读取':'修改'}</span></span><code>${esc(x.path.join(' '))}</code></button>`).join(''):empty('没有匹配的操作。');
  E('native-list').scrollTop=listScroll;
  E('native-list').querySelectorAll('[data-native-id]').forEach(b=>b.addEventListener('click',async()=>{
    if(b.dataset.nativeId===nativeSelected&&nativeSpec)return;
    try{if(nativeFormDirty&&!await confirmAction('当前表单有尚未执行的参数。放弃这份表单并切换操作？'))return;await nativeSelect(b.dataset.nativeId)}catch(error){toast(error)}
  }));
}
afterRender.native=async()=>{
  nativeFormDirty=false;unsavedChanges=()=>nativeFormDirty;beforeNavigate=async()=>!nativeFormDirty||await confirmAction('当前表单有尚未执行的参数。放弃这份表单并离开？');
  nativeList();nativeFilesRender();
  E('native-search').addEventListener('input',()=>{nativeSearch=E('native-search').value;nativeList()});
  E('native-group').addEventListener('change',()=>{nativeGroup=E('native-group').value;nativeList()});
  E('content').querySelectorAll('[data-native-job]').forEach(b=>b.addEventListener('click',()=>nativeWatch(b.dataset.nativeJob)));
  if(nativeSelected)await nativeSelect(nativeSelected);
};

function nativeInput(field,id,argument=false){
  const label=nativeLabels[field.name]||field.name,required=argument&&field.required;
  let input;
  if(!argument&&!field.value)input=`<label class="check-row"><input id="${id}" data-native-flag="${esc(field.name)}" type="checkbox">${esc(label)}</label>`;
  else{
    const multi=field.variadic||field.repeatable;let control;
    if(field.name==='--waker-id')control=`<select id="${id}"><option value="">选择 Waker</option>${options(state.wakers)}</select>`;
    else if(nativeSpec?.id==='mcp.add'&&field.name==='--transport')control=`<select id="${id}"><option value="">选择连接方式</option><option value="http">HTTP</option><option value="sse">SSE</option><option value="stdio">stdio · 本地命令</option></select>`;
    else if(field.choices?.length)control=`<select id="${id}">${field.choices.map(v=>`<option value="${esc(v)}">${esc(v)}</option>`).join('')}</select>`;
    else if(field.secret)control=`<input id="${id}" type="password" autocomplete="off" maxlength="60000">`;
    else if(multi||/(?:^|--)(content|text|message|script|body|value)(?:$|-)|^--(json|env|headers)$/.test(field.name)&&!nativeFileField(field))control=`<textarea id="${id}" rows="${multi?3:2}" maxlength="60000"${required?' required':''} placeholder="${esc(multi?'每行一个值；包含换行时使用 JSON 字符串数组':field.placeholder||'')}"></textarea>`;
    else control=`<input id="${id}" maxlength="60000"${required?' required':''} placeholder="${esc(field.placeholder||'')}">`;
    input=`<div class="native-field-label"><label for="${id}">${esc(label)}${required?' *':''}</label>${argument?'':`<input id="${id}-use" type="checkbox" aria-label="传递参数 ${esc(label)}" title="勾选后传递此参数；留空也会传递">`}</div>${control}`;
    if(isAdmin()&&(nativeFileField(field)||(argument&&/file|path|zip/.test(field.description))))input+=`<div class="row native-file-actions"><label class="button-link">上传文件<input type="file" data-native-upload="${id}" hidden></label><button type="button" data-native-output="${id}">保存为可下载文件</button></div>`;
  }
  return `<div class="native-field${/content|^--(json|env|headers)$/.test(field.name)&&!nativeFileField(field)?' wide':''}"${argument?'':` data-native-option="${esc(field.name)}"`} data-field-id="${id}">${input}<details class="native-field-help"><summary>${esc(field.name)}${required?' · 必填':''}</summary><p>${esc(field.description||'官方位置参数')}${argument?'':' · 勾选后才传递；填写时自动勾选。'}</p></details></div>`;
}
function nativeFormSections(spec){
  const sections={basic:[],connection:[],import:[],auth:[],version:[],advanced:[]};
  spec.arguments.forEach((f,i)=>sections.basic.push(nativeInput(f,'native-arg-'+i,true)));
  spec.options.forEach((f,i)=>sections[nativeSection(f,spec)].push(nativeInput(f,'native-opt-'+i)));
  const titles={basic:'参数',connection:'连接配置',import:'导入内容',auth:'请求头与 OAuth',version:'版本与幂等参数',advanced:'更多选项'};
  return Object.entries(sections).filter(([,fields])=>fields.length).map(([name,fields])=>{
    const advanced=['auth','version','advanced'].includes(name),tag=advanced?'details':'section';
    return `<${tag} class="native-section" data-native-section="${name}">${advanced?`<summary>${titles[name]} · ${fields.length}</summary>`:`<h3>${titles[name]}</h3>`}<div class="native-parameters">${fields.join('')}</div></${tag}>`;
  }).join('');
}
function nativeSyncMode(){
  if(nativeSpec?.id!=='mcp.add')return;
  const mode=E('native-mcp-mode').value,transportIndex=nativeSpec.options.findIndex(f=>f.name==='--transport'),transport=E('native-opt-'+transportIndex).value;
  E('native-form').querySelectorAll('[data-native-option]').forEach(field=>field.hidden=!nativeOptionActive(field.dataset.nativeOption,mode,transport));
  E('native-form').querySelectorAll('[data-native-section]').forEach(section=>section.hidden=![...section.querySelectorAll('[data-native-option]')].some(field=>!field.hidden));
}

async function nativeSelect(id){
  nativeSelected=id;nativeSpec=null;nativeGuard={};nativeRequest=null;nativeFormDirty=false;nativeList();
  const page=generation;await loadState();const spec=await api('native/operation?id='+encodeURIComponent(id));
  if(view!=='native'||generation!==page||id!==nativeSelected)return;
  nativeSpec=spec;const box=E('native-editor');box.hidden=false;
  const guarded=id.startsWith('waker.update.')||['skill.manage','skill.delete','skill.self-evolution','skill.rollback'].includes(id);
  // text-ok: todo 是官方命令分组，用于决定是否提供 Run 上下文输入。
  const needsRunContext=['atwaker','todo','goal'].includes(spec.group);
  E('native-welcome').hidden=true;
  box.innerHTML=`<header class="native-editor-heading"><h2>${esc(nativeTitle(spec))}</h2><span class="mono muted">qoderwake ${esc(spec.path.join(' '))}</span>${spec.panelPage&&canView(spec.panelPage)?`<a href="#${esc(spec.panelPage)}">常用管理页面</a>`:''}${!spec.available?`<p class="error-text">${esc(errors[spec.error]||spec.error)}</p>`:''}</header>
    <form id="native-form"><div class="native-form-body">${spec.id==='mcp.add'?`<div class="native-mode"><div><label for="native-mcp-mode">添加方式</label><select id="native-mcp-mode"><option value="fields">填写配置</option><option value="json">导入 JSON</option><option value="file">导入 JSON 文件</option><option value="all">全部参数</option></select></div><p>填写时自动选用参数。参数右侧的勾选框控制是否传递，包括空值。</p></div>`:''}${nativeFormSections(spec)}
    ${guarded?`<div class="row"><button id="native-load-version" type="button">读取当前版本</button><span id="native-version" class="mono muted">填写 Waker / 技能后读取</span></div>`:''}
    ${!spec.panelOnly?`<details class="native-execution"><summary>执行设置${needsRunContext?'与当前 Run 上下文':''}</summary><label for="native-timeout">最长等待时间（秒）</label><input id="native-timeout" type="number" value="300" min="10" max="86400">${isAdmin()?`<label class="check-row"><input id="native-interactive" type="checkbox">使用交互终端（登录或需要输入的命令）</label><label for="native-stdin">标准输入</label><textarea id="native-stdin" rows="2" autocomplete="off"></textarea><details><summary>当前 Run 上下文</summary><p>命令要求现有运行身份时，填写该 Run 的标识与凭据。</p>${spec.contextKeys.map((key,i)=>`<label for="native-context-${i}">${esc(nativeContextLabels[key])}</label><input id="native-context-${i}" type="${/TOKEN/.test(key)?'password':'text'}" autocomplete="off">`).join('')}</details>`:''}</details>`:''}</div>
    <div class="native-submit-bar"><span>${spec.effect==='read'?'读取当前资源':'执行前将确认目标与参数'}</span><button class="primary" id="native-submit"${spec.available?'':' disabled'}>${spec.effect==='read'?'读取':'检查并执行'}</button></div></form>
    <details class="native-reference"><summary>官方参数参考</summary><pre class="code">${esc(spec.help)}</pre>${spec.currentHelp?`<h3>当前版本的帮助</h3><pre class="code">${esc(spec.currentHelp)}</pre>`:''}</details>`;
  box.querySelectorAll('input,textarea,select').forEach(el=>el.addEventListener('input',()=>{
    const enable=E(el.id+'-use');if(enable)enable.checked=true;
    nativeRequest=null;nativeFormDirty=true;
    nativeSyncMode();
    const i=Number(el.id.replace('native-opt-',''));
    if(el.id.startsWith('native-opt-')&&['--waker-id','--skill-id'].includes(spec.options[i]?.name)){nativeGuard={};if(E('native-version'))E('native-version').textContent='请读取当前版本'}
  }));
  if(!isAdmin()){
    const index=spec.options.findIndex(f=>f.name==='--waker-id');
    if(index>=0){E('native-opt-'+index).required=true;E('native-opt-'+index+'-use').checked=true;E('native-opt-'+index+'-use').disabled=true}
  }
  box.querySelectorAll('[data-native-upload]').forEach(el=>el.addEventListener('change',()=>nativeUpload(el.files[0],el.dataset.nativeUpload).catch(e=>toast(e))));
  box.querySelectorAll('[data-native-output]').forEach(el=>el.addEventListener('click',()=>nativeOutputFile(el.dataset.nativeOutput).catch(e=>toast(e))));
  E('native-load-version')?.addEventListener('click',()=>nativeVersion().catch(e=>toast(e)));
  E('native-form').addEventListener('submit',nativeSubmit);
  nativeSyncMode();
  E('native-editor').querySelector('h2').setAttribute('tabindex','-1');
  E('native-editor').querySelector('h2').focus({preventScroll:true});
}
function nativeParameters(){
  const args={},opts={};
  nativeSpec.arguments.forEach((f,i)=>{const val=E('native-arg-'+i).value;if(val!==''||f.required)args[f.name]=nativeParameterValue(f,val)});
  nativeSpec.options.forEach((f,i)=>{const id='native-opt-'+i;if(nativeSpec.id==='mcp.add'&&E(id).closest('[data-native-option]').hidden)return;if(!f.value){if(E(id).checked)opts[f.name]=true}else if(E(id+'-use').checked)opts[f.name]=nativeParameterValue(f,E(id).value)});
  return {arguments:args,options:opts};
}
async function nativeVersion(){
  const p=nativeParameters().options,wid=p['--waker-id'];
  if(nativeSpec.id==='skill.manage'&&p['--conversation-id']){if(!p['--base-version-id'])throw new Error('群技能请填写读取时的 --base-version-id');nativeGuard={baseVersionId:p['--base-version-id']};E('native-version').textContent=p['--base-version-id'];return}
  if(!wid)throw new Error('先选择 Waker');
  if(nativeSpec.id.startsWith('waker.update.')){const d=await api('waker/detail?id='+encodeURIComponent(wid));nativeGuard={baseRevision:d.configRevision}}
  else{
    if(!p['--skill-id'])throw new Error('先填写技能 ID；创建新技能可直接提交');
    const d=await api('skill/content?waker='+encodeURIComponent(wid)+'&skill='+encodeURIComponent(p['--skill-id']));
    nativeGuard={baseVersionId:d.skill?.currentVersionId||d.currentVersionId||d.versionId};
    const i=nativeSpec.options.findIndex(f=>f.name==='--base-version-id');
    if(i>=0){E('native-opt-'+i).value=nativeGuard.baseVersionId;E('native-opt-'+i+'-use').checked=true}
  }
  E('native-version').textContent=nativeGuard.baseRevision||nativeGuard.baseVersionId||'未取得版本';
}
async function nativeSubmit(event){
  event.preventDefault();const button=E('native-submit');button.disabled=true;
  try{
    const parameters=nativeParameters(),id=nativeSpec.id;
    if(id.startsWith('waker.update.')&&!nativeGuard.baseRevision)throw new Error('请先读取当前版本');
    const groupVersion=id==='skill.manage'&&parameters.options['--conversation-id']&&parameters.options['--base-version-id'];
    const creating=id==='skill.manage'&&parameters.options['--action']==='create';
    if(['skill.manage','skill.delete','skill.self-evolution','skill.rollback'].includes(id)&&parameters.options['--skill-id']&&!creating&&!nativeGuard.baseVersionId&&!groupVersion)throw new Error('请先读取技能当前版本');
    if(id==='waker.delete'){await deletionAction('waker',parameters.options['--waker-id']);return}
    if(id==='skill.rollback'){
      await mutate('skill/rollback',{wakerId:parameters.options['--waker-id'],skillId:parameters.options['--skill-id'],versionId:parameters.options['--to'],reason:parameters.options['--reason']||'从官方操作页回滚',...nativeGuard},'将所选技能恢复到指定版本？');nativeGuard={};return;
    }
    const body={operation:id,revision:nativeSpec.revision,parameters,...nativeGuard,
      timeout:Number(E('native-timeout').value),interactive:E('native-interactive')?.checked||false,stdin:E('native-stdin')?.value||'',context:{}};
    nativeSpec.contextKeys.forEach((key,i)=>{const val=E('native-context-'+i)?.value;if(val)body.context[key]=val});
    if(nativeSpec.effect!=='read'){
      if(!await confirmAction(`执行 ${nativeSpec.path.join(' ')}？请核对表单中的目标与参数。此操作可能修改数据、运行程序或访问外部服务。`))return;
      body.confirm=id;
    }
    const encoded=JSON.stringify(body);if(!nativeRequest||nativeRequest.body!==encoded)nativeRequest={body:encoded,key:idempotencyKey()};
    const job=await api('native/execute',body,{'Idempotency-Key':nativeRequest.key});
    E('native-form').querySelectorAll('input[type=password]').forEach(el=>el.value='');if(E('native-stdin'))E('native-stdin').value='';
    nativeRequest=null;nativeGuard={};nativeFormDirty=false;if(E('native-version'))E('native-version').textContent='再次修改前请重新读取';await nativeWatch(job.id);
  }catch(error){toast(error)}finally{if(button.isConnected)button.disabled=false}
}
async function nativeWatch(id){
  clearTimeout(nativeTimer);nativeJob=id;nativeOffset=0;nativeOutput='';
  const box=E('native-job');if(!box)return;box.hidden=false;
  box.innerHTML=`<h2>执行结果</h2><p id="native-job-status"></p><pre id="native-job-output" class="code native-output" tabindex="0"></pre><p id="native-job-note" class="muted"></p><details id="native-job-error" hidden><summary>错误详情</summary><code id="native-job-error-code"></code></details><div class="row"><button id="native-cancel" type="button">停止本地命令</button><button id="native-copy" type="button">复制当前输出</button></div><form id="native-input-form" hidden><label for="native-input">终端输入</label><input id="native-input" type="password" autocomplete="off"><div class="row"><button type="submit">发送并换行</button><button id="native-eof" type="button">结束输入</button></div></form>`;
  E('native-cancel').addEventListener('click',async()=>{try{await api('native/cancel',{id});toast('已请求停止命令；已提交的官方任务请通过对应取消操作核对')}catch(e){toast(e)}});
  E('native-copy').addEventListener('click',()=>navigator.clipboard.writeText(nativeOutput).catch(()=>toast('浏览器未允许复制，请选择输出后手动复制')));
  E('native-input-form').addEventListener('submit',async e=>{e.preventDefault();try{await api('native/input',{id,input:E('native-input').value+'\n'});E('native-input').value=''}catch(error){toast(error)}});
  E('native-eof').addEventListener('click',()=>api('native/input',{id,input:'\x04'}).catch(e=>toast(e)));
  box.scrollIntoView({block:'nearest'});await nativePoll(id,generation);
}
async function nativePoll(id,page){
  if(view!=='native'||generation!==page||nativeJob!==id)return;
  try{
    const job=await api('native/job?id='+encodeURIComponent(id)+'&offset='+nativeOffset);
    if(view!=='native'||generation!==page||nativeJob!==id)return;
    nativeOffset=job.nextOffset;nativeOutput+=job.output;const outputBytes=new TextEncoder().encode(nativeOutput);if(outputBytes.length>2097152)nativeOutput=new TextDecoder().decode(outputBytes.slice(-2097152));
    E('native-job-status').textContent=`${job.operation} · ${nativeStateLabel(job.status)}${job.exit_code!==null?' · 退出码 '+job.exit_code:''}`;
    const pre=E('native-job-output'),stick=pre.scrollTop+pre.clientHeight>=pre.scrollHeight-60;pre.textContent=nativeOutput;if(stick)pre.scrollTop=pre.scrollHeight;
    const active=['queued','running'].includes(job.status);
    E('native-cancel').disabled=!active;E('native-input-form').hidden=!job.canInput;
    const note=nativeJobNote(job);E('native-job-note').textContent=note;E('native-job-note').hidden=!note;E('native-job-error').hidden=!job.error;E('native-job-error-code').textContent=job.error||'';
    if(active||job.output.length===65536)nativeTimer=setTimeout(()=>nativePoll(id,page),700);
  }catch(error){if(view==='native'&&generation===page)toast(error)}
}
async function nativeUpload(file,id){
  if(!file)return;if(file.size>67108864)throw new Error('上传文件最大 64 MiB');
  const slot=await api('native/file/create',{name:file.name});nativeFiles.push(slot);nativeFilesRender();
  for(let offset=0;offset<file.size;offset+=32768){const bytes=new Uint8Array(await file.slice(offset,offset+32768).arrayBuffer());await api('native/file/append',{id:slot.id,offset,data:btoa(String.fromCharCode(...bytes))})}
  nativeUseFile(id,slot.reference);toast('文件已上传');
}
async function nativeOutputFile(id){
  const field=E(id),name=field.value.trim()||'result.json';
  if(name.includes('/')||name.includes('\\')||name.startsWith('@panel-file:'))throw new Error('先在参数中填写下载文件名，例如 export.zip');
  const slot=await api('native/file/create',{name,output:true});nativeFiles.push(slot);nativeFilesRender();nativeUseFile(id,slot.reference);
}
function nativeUseFile(id,reference){const field=E(id);if(!field)return;const i=Number(id.replace('native-opt-','')),f=nativeSpec.options[i];field.value=f?.repeatable?JSON.stringify([reference]):reference;const enable=E(id+'-use');if(enable)enable.checked=true;nativeRequest=null;nativeFormDirty=true}
function nativeFilesRender(){
  const box=E('native-files');if(!box)return;box.hidden=!nativeFiles.length;
  box.innerHTML=`<h2>操作文件</h2>${table(['文件','用途','操作'],nativeFiles.map(f=>`<tr><td>${esc(f.name)}</td><td>${f.output?'输出文件':'上传文件'}</td><td><a href="/api/native/file?id=${f.id}">下载</a> <button type="button" data-native-remove="${f.id}">清理</button></td></tr>`))}`;
  box.querySelectorAll('[data-native-remove]').forEach(b=>b.addEventListener('click',async()=>{try{await api('native/file/delete',{id:b.dataset.nativeRemove});nativeFiles=nativeFiles.filter(f=>f.id!==b.dataset.nativeRemove);nativeFilesRender()}catch(e){toast(e)}}));
}
Object.assign(errors,{cli_binary_unavailable:'未找到官方 CLI，请检查 QW_DAEMON_BIN',cli_help_unavailable:'无法读取此命令帮助，请检查当前 CLI 版本',cli_contract_changed:'当前 CLI 参数已变化，请更新对应命令参考后使用',cli_use_panel_action:'请从对应管理页面执行，保留删除检查与版本校验',cli_request_key_required:'请求标识缺失，请重新打开操作表单',cli_argument_required:'请填写必需的位置参数',cli_argument_gap:'可选位置参数需要按顺序填写',invalid_cli_parameters:'参数格式不正确',unknown_cli_parameter:'参数未在当前命令中登记',cli_scoped_option_rejected:'此参数需要管理员权限',cli_file_not_ready:'输出文件尚未生成，请等待命令结束',cli_file_too_large:'文件超过 64 MiB 上限',cli_cancelled:'本地命令已停止；已提交给官方的修改请核对资源状态',cli_timeout:'命令等待超时；已提交的修改需要核对，不会自动重发',panel_restarted:'Panel 已重启，先前操作结果需要核对',cli_job_finished:'该命令已结束',cli_input_not_ready:'终端尚未就绪，请稍后重试',cli_execution_failed:'命令执行失败，请检查官方 CLI 路径与运行环境',cli_instance_mismatch:'CLI 的 HOME / 端口与当前 Panel 连接目标不一致，请检查启动配置',cli_file_slots_full:'临时文件已达 100 个，请先清理不再使用的文件'});
}
