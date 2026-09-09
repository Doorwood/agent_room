const status=document.getElementById('status');
try {
 const response=await fetch('/info',{cache:'no-store'});if(!response.ok)throw new Error('本地检测不可用');
 const info=await response.json();if(info.app!=='agent_room')throw new Error('无法识别本机服务');
 const dashboardURL=new URL(info.url);
 if(dashboardURL.protocol!=='http:' || dashboardURL.hostname!=='127.0.0.1')throw new Error('本地 Dashboard 地址无效');
 status.textContent='本机 Dashboard 正在运行 · '+info.version;
 const link=document.getElementById('dashboard');link.href=dashboardURL.href;link.hidden=false;
 const params=new URLSearchParams(location.hash.slice(1));
 const nonce=params.get('nonce'),origin=params.get('origin');
 if(window.opener && /^[a-f0-9]{32}$/.test(nonce || '') && origin){
  const target=new URL(origin);
  if(['http:','https:'].includes(target.protocol)&&target.origin===origin){const send=state=>window.opener?.postMessage({app:'agent_room',version:info.version,nonce,...(state?{state}:{})},origin);
   if(params.get('action')!=='join'){send();}else{
    const request={address:params.get('address')||'',session:params.get('session')||'',name:params.get('name')||''};
    if(!request.address || !request.session || !request.name || JSON.stringify(request).length>3000)throw new Error('申请参数无效，请回到项目页面重试');
    document.getElementById('join-panel').hidden=false;
    document.getElementById('join-target').textContent='Host：'+request.address+'\n昵称：'+request.name+'\nSession：'+request.session;
    status.textContent='请核对信息并确认，尚未提交申请。';send('confirm');
    const button=document.getElementById('confirm-join');
    const labels={connecting:'正在连接 Host…',pending:'申请已提交，等待 Host 审批。',connected:'已获批准并连接，请打开本机 Dashboard 进入对话。',error:'连接失败或申请被拒绝，请在本机 Dashboard 查看详情。',disconnected:'连接已断开，可在本机 Dashboard 重试。',disconnecting:'正在断开连接…'};
    async function update(submit){
     const response=await fetch(submit?'/join':'/join-status',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(request)});
     if(!response.ok)throw new Error('申请状态无法确认，请打开本机 Dashboard 检查，或核对连接信息后重试。');
     const reply=await response.json();
     if(!Object.hasOwn(labels,reply.state))throw new Error('无法识别申请状态，请查看本机 Dashboard。');
     status.textContent=labels[reply.state];send(reply.state);
     if(['connecting','pending','disconnecting'].includes(reply.state))setTimeout(()=>update(false).catch(failed),1000);
    }
    function failed(error){status.textContent=error.message;send('unknown');button.disabled=false;}
    button.onclick=()=>{button.disabled=true;update(true).catch(failed);};
   }}
 }
} catch(error){status.textContent=error.message;}
