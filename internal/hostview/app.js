const status=document.getElementById('page-status');
let roomInfo;
const quote=value=>"'"+String(value).replaceAll("'","'\\''")+"'";
try {
 const response=await fetch('/info',{cache:'no-store'});
 if(!response.ok)throw new Error('Host 信息读取失败，请刷新重试');
 const info=await response.json();roomInfo=info;document.getElementById("apply").disabled=false;
 document.getElementById('project').textContent=info.project;
 document.getElementById('address').textContent=info.address;
 document.getElementById('session').textContent=info.session;
 document.getElementById('host-version').textContent='Host 版本 '+(info.version || '未知');
 document.getElementById('join').textContent='agent_room answers '+quote(info.address)+' '+quote(info.session)+' --name YOUR_NAME';
 document.querySelectorAll('[data-copy]').forEach(b=>b.disabled=false);
} catch(error) { status.textContent=error.message; }
for(const button of document.querySelectorAll('[data-copy]'))button.onclick=async()=>{
 const node=document.getElementById(button.dataset.copy);
 try {
  if(!navigator.clipboard?.writeText)throw new Error('manual');
  await navigator.clipboard.writeText(node.textContent);button.textContent='已复制';
 }catch{
  const range=document.createRange();range.selectNodeContents(node);const selection=getSelection();selection.removeAllRanges();selection.addRange(range);button.textContent='已选中，请手动复制';
 }
};
const detect=document.getElementById('detect'),result=document.getElementById('detect-status');
let pending=null;
const origin='http://127.0.0.1:18743';
detect.onclick=()=>{
 if(pending)return;
 const nonce=Array.from(crypto.getRandomValues(new Uint8Array(16)),n=>n.toString(16).padStart(2,'0')).join('');
 const popup=window.open(origin+'/check#'+new URLSearchParams({nonce,origin:location.origin}),'agent-room-local-check','popup,width=620,height=500');
 if(!popup){result.textContent='检测页被浏览器拦截，请允许打开弹窗后重试。也可以在终端执行 agent_room --version 确认安装。';return;}
 detect.disabled=true;
 result.textContent='正在等待本机 Dashboard 响应…';
 const timer=setTimeout(()=>{
  pending=null;detect.disabled=false;
  result.textContent='未检测到运行中的 Dashboard：可能尚未安装、尚未启动、版本较旧，或检测被浏览器拦截。请先执行 agent_room --version；若已安装，启动或更新 Dashboard 后再试。';
 },5000);
 pending={nonce,popup,timer};
};
window.addEventListener('message',event=>{
 if(!pending || event.origin!==origin || event.source!==pending.popup)return;
 const data=event.data;
 if(!data || data.app!=='agent_room' || data.nonce!==pending.nonce || typeof data.version!=='string' || data.version.length>100)return;
 clearTimeout(pending.timer);pending=null;detect.disabled=false;
 result.textContent='已检测到本机 Dashboard · '+data.version+'。请在检测页点击「打开本机 Dashboard」，填写上方连接信息并申请加入。';
});


const apply=document.getElementById('apply'),applyStatus=document.getElementById('apply-status');
let application=null;
const states={confirm:'请在本机窗口核对信息并确认，尚未提交申请。',connecting:'正在连接 Host…',pending:'申请已提交，等待 Host 审批。',connected:'已获批准并连接！请在本机窗口打开 Dashboard 进入对话。',error:'连接失败或申请被拒绝，请在本机 Dashboard 查看详情。',disconnected:'连接已断开，请在本机 Dashboard 查看或重试。',disconnecting:'正在断开连接…',unknown:'无法确认申请状态，请在本机 Dashboard 查看。'};
apply.onclick=()=>{
 const name=document.getElementById('join-name').value.trim();
 if(!name || new TextEncoder().encode(name).length>64 || /[\p{Cc}\p{Cf}]/u.test(name)){applyStatus.textContent='请输入 1–64 字节的昵称，不含控制字符。';return;}
 if(application && !application.popup.closed){application.popup.focus();return;}
 const nonce=Array.from(crypto.getRandomValues(new Uint8Array(16)),n=>n.toString(16).padStart(2,'0')).join('');
 const popup=window.open(origin+'/check#'+new URLSearchParams({nonce,origin:location.origin,action:'join',address:roomInfo.address,session:roomInfo.session,name}),'agent-room-local-join','popup,width=620,height=650');
 if(!popup){applyStatus.textContent='申请窗口被拦截，请允许弹窗后重试。尚未提交申请。';return;}
 applyStatus.textContent='正在打开本机确认窗口…';
 const timer=setTimeout(()=>{application=null;applyStatus.textContent='无法确认申请状态：请先安装或更新并启动本机 Dashboard 后重试。如已确认提交，请在本机 Dashboard 查看。';},5000);
 application={nonce,popup,timer};
};
window.addEventListener('message',event=>{
 if(!application || event.origin!==origin || event.source!==application.popup)return;
 const data=event.data;
 if(!data || data.app!=='agent_room' || data.nonce!==application.nonce || !Object.hasOwn(states,data.state))return;
 clearTimeout(application.timer);applyStatus.textContent=states[data.state];
});
