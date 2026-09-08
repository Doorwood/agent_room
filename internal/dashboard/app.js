const $ = id => document.getElementById(id);
const labels = {disconnected:'未连接',connecting:'连接中',pending:'等待审批',connected:'已连接',reconnecting:'正在重连',disconnecting:'正在断开',error:'连接失败'};
let data = [], busy = false, signature = '';
async function post(action, body) {
 const response = await fetch(action,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body),signal:AbortSignal.timeout(12000)});
 if (!response.ok) throw new Error(await response.text());
 return response.json();
}
function render() {
 const query = $('search').value.trim().toLowerCase();
 const filtered = data.filter(r=>[r.project,r.address,r.name,r.session].join(' ').toLowerCase().includes(query));
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
  const title = document.createElement('h3');title.textContent=room.project?.split('/').filter(Boolean).at(-1) || room.address;
  const meta = document.createElement('p');meta.className='meta';meta.textContent=room.address+' · '+(room.name || '连接时选择昵称');
  const details = document.createElement('details');const summary = document.createElement('summary');summary.textContent='Room 信息';
  const text = document.createElement('p');text.className='details-text';text.textContent=(room.project?'项目：'+room.project+'\n':'')+'Session：'+room.session;
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
  if (room.url) {
   const link=document.createElement('a');link.className='open';link.textContent='打开对话 ↗';link.href=room.url;link.target='_blank';link.rel='noopener noreferrer';actions.append(link);
  }
  card.append(top,title,meta,details,note,actions);$('rooms').append(card);
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
  const next=JSON.stringify(state.rooms);
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
