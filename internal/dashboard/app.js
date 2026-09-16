const $ = id => document.getElementById(id);
const labels = {disconnected:'未连接',connecting:'连接中',pending:'等待审批',connected:'已连接',reconnecting:'正在重连',disconnecting:'正在断开',error:'连接失败'};
let localAgents=[], installedAgents=[], invitationRoom=null, invitationAttempt=null;
let data = [], busy = false, signature = '';
async function post(action, body) {
 const response = await fetch(action,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body),signal:AbortSignal.timeout(12000)});
 if (!response.ok) throw new Error(await response.text());
 return response.json();
}
function render() {
 const query = $('search').value.trim().toLowerCase();
 const filtered = data.filter(r=>[r.projectName,r.project,r.address,r.name,r.session].join(' ').toLowerCase().includes(query));
 $('count').textContent = data.length;
 $('rooms').replaceChildren();
 $('empty').hidden = filtered.length>0;
 $('empty').querySelector('h2').textContent = data.length ? '没有匹配的 room' : '还没有 room';
 for (const room of filtered) {
  const card = document.createElement('article');card.className='room';
  const top = document.createElement('div');top.className='room-top';
  const kind = document.createElement('span');kind.className='kind';kind.textContent=room.kind==='owned'?'本机项目':'成员身份';
  const status = document.createElement('span');status.className='status '+room.status;status.textContent=labels[room.status] || room.status;
  top.append(kind,status);
  const title = document.createElement('h3');title.textContent=room.projectName || room.project?.split('/').filter(Boolean).at(-1) || '项目名称待同步';
  const meta = document.createElement('p');meta.className='meta';meta.textContent=room.address+' · '+(room.name || '连接时选择昵称');
  const details = document.createElement('details');const summary = document.createElement('summary');summary.textContent='Room 信息';
  const text = document.createElement('p');text.className='details-text';text.textContent='项目名称：'+(room.projectName || '连接后自动同步')+'\n'+(room.project?'项目路径：'+room.project+'\n':'')+'Session：'+room.session;
  details.append(summary,text);
  const note = document.createElement('p');note.className='note';note.setAttribute('role','status');note.textContent=room.detail || '';
  const actions = document.createElement('div');actions.className='actions';
  const active = ['connecting','pending','connected','reconnecting','disconnecting'].includes(room.status);
  let name;
  if (!room.name && !active) {name=document.createElement('input');name.placeholder='连接昵称（首次需审批）';name.setAttribute('aria-label','连接昵称');name.maxLength=64;actions.append(name);}
  const button = document.createElement('button');button.textContent=active?'断开连接':'连接';button.className=active?'secondary':'';button.disabled=room.status==='disconnecting';
  button.addEventListener('click',async()=>{
   button.disabled=true;
   try {await post(active?'disconnect':'connect',{id:room.id,name:name?.value || ''});signature='';await refresh();}
   catch(error){note.textContent=error.message;button.disabled=false;}
  });
  actions.append(button);
  const invite=document.createElement('button');invite.className='secondary';invite.textContent='邀请本机 Agent';invite.disabled=room.status!=='connected';invite.onclick=()=>openAgentInvitation(room);actions.append(invite);
  const workers=document.createElement('div');workers.className='local-agent-list';
  for(const a of localAgents.filter(a=>a.roomId===room.id)){
    const row=document.createElement('p');row.textContent=(a.name || a.provider)+' · '+({checking:'健康检查中',idle:'健康 · 待命',working:'工作中',offline:'离线',error:'需检查'}[a.state] || a.state)+' · @agent:'+a.workerId+(a.detail?' · '+a.detail:'');
    const leave=document.createElement('button');leave.className='secondary';leave.textContent='移除 Agent';leave.onclick=async()=>{try{await post('agent-leave',{id:a.id});signature='';await refresh();}catch(e){note.textContent=e.message}};row.append(leave);
    if(a.lastHealthy && !a.lastHealthy.startsWith('0001')){const info=document.createElement('span');info.textContent=' · 最近成功 '+new Date(a.lastHealthy).toLocaleString()+' · 检查 '+((a.checkMillis||0)/1000).toFixed(1)+' 秒';row.append(info);}
    if(a.state==='idle'||a.state==='error'){const retry=document.createElement('button');retry.className='secondary';retry.textContent='重新检查';retry.onclick=async()=>{try{await post('agent-recheck',{id:a.id});signature='';await refresh()}catch(e){note.textContent=e.message}};row.append(retry)}workers.append(row);
  }
  if (room.url) {
   const link=document.createElement('a');link.className='open';link.textContent='打开对话 ↗';link.href=room.url;link.target='_blank';link.rel='noopener noreferrer';actions.append(link);
  }
  const remove=document.createElement('button');remove.type='button';remove.className='secondary danger';remove.textContent='删除 Room';
  remove.addEventListener('click',async()=>{
   if(!confirm('从本机 Dashboard 删除此 Room？会断开本 Dashboard 的连接，保留成员身份和 host 上的项目、聊天记录。之后可重新添加。'))return;
   remove.disabled=true;
   try{await post('remove',{id:room.id});signature='';await refresh();}catch(error){note.textContent=error.message;remove.disabled=false;}
  });actions.append(remove);
  card.append(top,title,meta,details,note,actions,workers);$('rooms').append(card);
 }
}
async function refresh() {
 if (busy) return;busy=true;
 try {
  const response=await fetch('rooms',{cache:'no-store',signal:AbortSignal.timeout(10000)});
  if (!response.ok) throw new Error('无法读取 room 列表');
  const state=await response.json();
  $('service-status').textContent='';$('version').textContent='本机客户端 v'+state.version;
  $('warnings').textContent=(state.warnings || []).join('\n');
  const agentsResponse=await fetch('agents',{cache:'no-store',signal:AbortSignal.timeout(10000)});
  if(agentsResponse.ok){const agents=await agentsResponse.json();localAgents=agents.running || [];installedAgents=agents.installed || [];}
  const next=JSON.stringify([state.rooms,localAgents]);
  if (next!==signature) {
   // Do not replace an in-progress nickname input during background polling.
   if (!$('rooms').contains(document.activeElement) || document.activeElement.tagName!=='INPUT') {data=state.rooms;signature=next;render();}
  }
 } catch(error) {$('service-status').textContent='本机 Dashboard 已断开，请重新运行 agent_room dashboard 并打开新地址。';}
 finally {busy=false;}
}
$('join-form').addEventListener('submit',async event=>{
 event.preventDefault();$('join').disabled=true;$('join-status').textContent='正在保存…';
 try {
  const room=await post('add',{address:$('address').value,session:$('session').value,name:$('name').value});
  await post('connect',{id:room.id});$('join-status').textContent='已保存；请在下方查看连接或审批状态。';signature='';await refresh();
 }catch(error){$('join-status').textContent=error.message;}
 finally{$('join').disabled=false;}
});
$('search').addEventListener('input',render);$('refresh').addEventListener('click',()=>{signature='';refresh();});
refresh();setInterval(refresh,2000);

function openAgentInvitation(room){
 invitationRoom=room;invitationAttempt=null;$('invite-agent-name').value='';$('invite-room-name').textContent=room.projectName || room.address;$('invite-project').value=room.kind==='owned'?(room.project || ''):'';$('invite-scope').value='host';updateInviteScope();$('invite-mode').value='review';$('invite-agent-status').textContent='';
 const select=$('invite-provider');select.replaceChildren();for(const a of installedAgents){const option=new Option(a.name+(a.installed?' · 已检测到':' · 未安装'),a.provider);option.disabled=!a.installed;select.add(option)};select.value=installedAgents.find(a=>a.installed)?.provider || '';
 $('agent-detection').textContent='检测到 CLI 不代表已登录。请在本机完成 codex login、cursor-agent login 或 claude auth login。';$('invite-agent-submit').disabled=!select.value;$('invite-agent-dialog').showModal();
}
$('invite-agent-close').onclick=()=>$('invite-agent-dialog').close();
$('invite-agent-form').onsubmit=async e=>{
 e.preventDefault();const body={id:invitationRoom.id,agentName:$('invite-agent-name').value.trim(),provider:$('invite-provider').value,workspaceScope:$('invite-scope').value,project:$('invite-scope').value==='local'?$('invite-project').value:'',mode:$('invite-mode').value};
 if(!invitationAttempt || JSON.stringify(invitationAttempt.body)!==JSON.stringify(body))invitationAttempt={body,request:{...body,invitation:crypto.randomUUID().replaceAll('-','')}};
 $('invite-agent-submit').disabled=true;$('invite-agent-status').textContent='正在向 Host 注册本机 Agent…';
 try{await post('agent-invite',invitationAttempt.request);$('invite-agent-dialog').close();signature='';await refresh();}catch(error){$('invite-agent-status').textContent=error.message;}finally{$('invite-agent-submit').disabled=false;}
};

function updateInviteScope(){const local=$('invite-scope').value==='local';$('invite-project-label').hidden=!local;$('invite-project').required=local;}
$('invite-scope').onchange=updateInviteScope;
