const get=id=>document.getElementById(id);
const dialog=get('resource-dialog');
let mode='',role='',busy=false;
async function info(){
 const r=await fetch('resources');if(!r.ok)throw new Error(await r.text());
 const s=await r.json();mode=s.mode;role=s.role;
 get('resource-info').textContent='Room 资源授权：'+(mode==='personal'?'个人授权':'Host 授权')+' · 本机授权'+(s.enabled?'已开启':'未开启')+(s.busy?' · 调用中':'');
 get('resource-form').hidden=role==='visitor'||(role==='asker'&&mode==='host');
 if(role==='asker'&&mode==='host')get('resource-info').textContent+='；询问者使用资源功能需要 Host 切换个人授权。';
 get('resource-share').hidden=role!=='roommate';
 get("feishu-create-option").disabled=!s.canCreate;
}
get('resource-open').onclick=()=>{dialog.showModal();info().catch(e=>get('resource-info').textContent=e.message);};
get('resource-close').onclick=()=>dialog.close();
async function post(body){
 const r=await fetch('resources',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
 if(!r.ok)throw new Error(await r.text());return r.json();
}
async function grant(enabled){
 try{await post({action:'grant',enabled,root:get('resource-root').value.trim()});get('resource-status').textContent=enabled?'本机授权已开启，每次调用仍需核对确认。':'本机授权已撤销，进行中的请求已停止。';await info();}
 catch(e){get('resource-status').textContent=e.message;}
}
get('resource-enable').onclick=()=>grant(true);get('resource-revoke').onclick=()=>grant(false);
get('resource-form').onsubmit=async event=>{
 event.preventDefault();if(busy)return;busy=true;get('resource-submit').disabled=true;
 const resource={action:get('resource-action').value,target:get('resource-target').value.trim(),query:get('resource-query').value.trim()};
 if(resource.action==='feishu.search')resource.target='';
 try{
  await info();
  if(resource.action==='feishu.create'){
   if(mode!=='personal'||role!=='roommate')throw new Error('创建文档必须使用个人模式和协作成员身份。');
   const account=await post({action:'feishu-identity'});
   get('resource-account').textContent='本机飞书账号：'+account.name+' · '+account.openId;
   const payload={action:'feishu.create',target:'',title:get('resource-title').value.trim(),content:get('resource-content').value,accountId:account.openId};
   if(new TextEncoder().encode(payload.title).length>240||new TextEncoder().encode(payload.content).length>24000)throw new Error('标题最多 240 字节，正文最多 24,000 字节，请缩短后创建。');
   if(!confirm('创建新的飞书文档\n账号：'+account.name+' · '+account.openId+'\n标题：'+payload.title+'\n正文：'+payload.content.length+' 字符（请先核对上方完整正文）\n只使用本机个人身份，是否创建？'))return;
   const signature=JSON.stringify(payload);
   if(!createAttempt||createAttempt.signature!==signature)createAttempt={signature,requestId:crypto.randomUUID().replaceAll('-','')};
   saveCreateAttempt();get('resource-document-link').hidden=true;get('resource-result').hidden=true;
   get('resource-status').textContent='正在使用本机个人飞书账号创建…';
   const reply=await post({action:'query',mode:'personal',confirmed:true,resource:{...payload,requestId:createAttempt.requestId}});
   showReceipt(JSON.parse(reply.text));return;
  }
  if(!confirm('使用'+(mode==='personal'?'你本机的个人身份':'Host 身份')+'执行只读操作：\n'+get('resource-action').selectedOptions[0].textContent+'\n'+resource.target+(resource.query?'\n搜索词：'+resource.query:'')+'\n\n结果会返回 Host；填写问题时会交给独立模型分析。是否继续？'))return;
  get('resource-result').hidden=true;
  get('resource-status').textContent='正在读取资源并处理问题…';
  const reply=await post({action:'query',mode,confirmed:true,resource,question:get('resource-question').value});
  get('resource-raw').textContent=reply.text||'';get('resource-answer').textContent=reply.answer||reply.text||'资源返回为空';
  get('resource-result').hidden=false;get('resource-status').textContent=reply.summaryError||'已完成，仅此窗口显示。';await info();
 }catch(e){get('resource-status').textContent=e.message;}
 finally{busy=false;get('resource-submit').disabled=false;}
};
get('resource-share').onclick=()=>{
 const selection=getSelection()?.toString().trim();
 const text=selection||get('resource-answer').textContent;
 if(role!=='roommate'||!text)return;
 const input=get('message');
 if(!confirm('将这些内容带入主聊天草稿，发送后所有成员和主模型都能看到。是否继续？'))return;
 if(input.value.trim()){get('resource-status').textContent='主聊天已有草稿，请先处理后再分享。';return;}
 if(text.length>10000){get('resource-status').textContent='内容过长，请先选中需要分享的片段。';return;}
 input.value='[明确分享的资源内容]\n'+text;input.dispatchEvent(new Event('input',{bubbles:true}));
 dialog.close();get('main-view').click();input.focus();
};

let createAttempt=null;
const attemptKey='agent-room-document-attempt:'+location.pathname;
try{createAttempt=JSON.parse(sessionStorage.getItem(attemptKey)||'null');}catch{}
function saveCreateAttempt(){
 try{sessionStorage.setItem(attemptKey,JSON.stringify(createAttempt));}catch{}
 get('resource-receipt').hidden=!createAttempt;
 get('resource-receipt-id').textContent=createAttempt?'本机创建回执：'+createAttempt.requestId:'';
}
saveCreateAttempt();
function changeAction(){
 const write=get('resource-action').value==='feishu.create';
 get('resource-create-fields').hidden=!write;
 for(const id of ['resource-target','resource-query','resource-question']){
  get(id).hidden=write;document.querySelector('label[for="'+id+'"]').hidden=write;
 }
 get('resource-title').required=write;get('resource-content').required=write;
 get('resource-submit').textContent=write?'核对账号并创建文档':'核对并读取资源';
}
get('resource-action').onchange=changeAction;
function showReceipt(receipt){
 if(!receipt || !['completed','unknown'].includes(receipt.state))throw new Error('无法识别创建回执，请在自己的飞书中核对。');
 get('resource-status').textContent=receipt.state==='completed'?'已使用 '+receipt.account.name+' 的个人飞书账号创建文档。':'创建结果待核实，可能已经创建；不会自动重试。请在该账号飞书中核对，或查询本机回执。';
 get('resource-account').textContent='创建账号：'+receipt.account.name+' · '+receipt.account.openId;
 const link=get('resource-document-link');link.hidden=true;link.removeAttribute('href');
 if(receipt.url){
  try{const u=new URL(receipt.url);if(u.protocol==='https:'&&!u.username&&!u.password&&['feishu.cn','larkoffice.com','larksuite.com','doubao.com'].some(d=>u.hostname===d||u.hostname.endsWith('.'+d))){link.href=u.href;link.hidden=false;}}catch{}
 }
}
get('resource-receipt').onclick=async()=>{
 if(!createAttempt)return;
 try{showReceipt(await post({action:'receipt',resource:{requestId:createAttempt.requestId}}));}
 catch(e){get('resource-status').textContent=e.message+'；请在自己的飞书中核对，切勿据此认定未创建。';}
};
window.addEventListener('agent-room-create-document',async event=>{
 if(busy)return;
 try{
  if(!dialog.open)dialog.showModal();
  await info();
  if(mode!=='personal'||role!=='roommate'){get('resource-status').textContent='此操作需要个人权限模式和协作成员身份；没有使用 Host 创建。';return;}
  if(get('resource-content').value.trim()&&!confirm('创建区已有正文，是否替换为这条模型回复？'))return;
  get('resource-action').value='feishu.create';changeAction();
  const text=String(event.detail?.text||'');
  if(new TextEncoder().encode(text).length>24000)throw new Error('正文超过 24,000 字节，请先选取需要创建的内容；未截断原文。');
  get('resource-content').value=text;
  get('resource-status').textContent='已带入正文草稿，请填写标题、核对全文和本机飞书账号后创建。';
 }catch(e){get('resource-status').textContent=e.message;}
});

// A model request arrives on the authenticated sender's local window only.
const personalDialog=get('personal-dialog');
let personalPending=null,personalAccount=null,personalShown='',personalBusy=false,pushAccount=null;
async function personalPost(body){
 const r=await fetch('personal',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
 if(!r.ok)throw new Error(await r.text());return r.json();
}
async function personalPoll(){
 try{
  const r=await fetch('personal');if(!r.ok)return;const s=await r.json();
  const changed=personalPending?.id!==s.pending?.id;personalPending=s.pending;
  get('personal-banner').hidden=!s.pending;
  get('personal-banner-text').textContent=s.status||'';
  get('personal-status').textContent=s.status||'';
  get('personal-decline').disabled=!!s.busy;get('personal-identity').disabled=!!s.busy;
  if(changed){pushAccount=null;get('personal-push-account').textContent='';get('personal-push-grant').disabled=true;personalAccount=null;get('personal-account').textContent='';get('personal-grant').disabled=true;}
  if(!s.pending){if(personalDialog.open&&!personalBusy)personalDialog.close();return;}
  get('personal-sender').textContent='本次请求来自：'+s.pending.sender;
  get('personal-title').textContent=s.pending.title;get('personal-content').textContent=s.pending.content;
  const git=s.pending.action==='git.commit',push=s.pending.action==='git.push',append=s.pending.action==='feishu.append';
  get('personal-heading').textContent=push?'确认你的 GitHub 推送':git?'确认你的 Git 提交署名':append?'确认向已有飞书文档补充正文':'你的飞书文档创建请求';
  get('personal-feishu-description').textContent=append?'使用你的本机飞书账号向指定文档末尾补充正文，保留已有内容。每次补写单独确认，不复用创建许可。':'仅使用你的本机飞书账号。授权后本次连接中由你要求创建的新文档可自动创建，其他成员不能借用；断开或撤销后失效。';
  get('personal-grant').textContent=append?'确认目标并补充正文':'授权我的账号并继续';
  get('personal-content-label').textContent=(git||push)?'核对提交范围':'核对完整正文';
  get('personal-feishu').hidden=git||push;get('personal-git').hidden=!git;get('personal-push').hidden=!push;
  get('personal-push-check').disabled=!!s.busy;get('personal-push-grant').disabled=!!s.busy||!pushAccount;
  if(push&&s.pending.push)get('personal-content').textContent=s.pending.content+'\n\n'+s.pending.push.summary+'\n提交包：'+Math.ceil(s.pending.push.packSize/1024)+' KiB';
  if(append)get('personal-content').textContent='目标文档：'+s.pending.target+'\n操作：末尾追加，保留原内容\n\n'+s.pending.content;
  const authorized=push?!!s.pushApproved:git?!!s.gitIdentity:append?!!s.appendApproved:!!s.accountId;
  if(!authorized&&personalShown!==s.pending.id&&!document.querySelector('dialog[open]')){personalShown=s.pending.id;personalDialog.showModal();}
 }catch{}finally{setTimeout(personalPoll,1200);}
}
get('personal-open').onclick=()=>{if(personalPending&&!personalDialog.open)personalDialog.showModal();};
get('personal-close').onclick=()=>personalDialog.close();
get('personal-identity').onclick=async()=>{
 if(personalBusy)return;personalBusy=true;
 try{
  const a=await post({action:'feishu-identity'});personalAccount=a;
  get('personal-account').textContent='将使用：'+a.name+' · '+a.openId;
  get('personal-grant').disabled=false;
 }catch(e){personalAccount=null;get('personal-account').textContent=e.message;get('personal-grant').disabled=true;}
 finally{personalBusy=false;}
};
get('personal-grant').onclick=async()=>{
 if(personalBusy||!personalPending||!personalAccount)return;personalBusy=true;get('personal-grant').disabled=true;
 try{await personalPost({action:'grant',id:personalPending.id,accountId:personalAccount.openId});personalShown='';personalAccount=null;get('personal-account').textContent='';personalDialog.close();}
 catch(e){get('personal-account').textContent=e.message;personalAccount=null;}
 finally{personalBusy=false;}
};
get('personal-decline').onclick=async()=>{
 if(personalBusy||!personalPending)return;personalBusy=true;
 try{await personalPost({action:'decline',id:personalPending.id});personalDialog.close();}
 catch(e){get('personal-account').textContent=e.message;}
 finally{personalBusy=false;}
};
personalPoll();

get('personal-git-grant').onclick=async()=>{
 if(personalBusy||personalPending?.action!=='git.commit')return;
 const identity={name:get('personal-git-name').value.trim(),email:get('personal-git-email').value.trim()};
 if(!identity.name||!identity.email||!get('personal-git-email').checkValidity()){get('personal-git-status').textContent='请填写有效的姓名和邮箱。';return;}
 personalBusy=true;get('personal-git-grant').disabled=true;
 try{await personalPost({action:'git-grant',id:personalPending.id,gitIdentity:identity});personalShown='';get('personal-git-status').textContent='';personalDialog.close();}
 catch(e){get('personal-git-status').textContent=e.message;}
 finally{personalBusy=false;get('personal-git-grant').disabled=false;}
};

get('personal-push-check').onclick=async()=>{
 if(personalBusy||personalPending?.action!=='git.push')return;
 personalBusy=true;pushAccount=null;get('personal-push-grant').disabled=true;
 const id=personalPending.id;
 try{
  const a=await personalPost({action:'push-check',id});
  if(personalPending?.id!==id)return;
  pushAccount=a.account;
  get('personal-push-account').textContent='GitHub 账号：'+a.account.login+'（ID '+a.account.id+'）\n目标：'+a.offer.input.repository+' · '+a.offer.input.branch+'\n提交：'+a.offer.input.commit+'\n远端当前：'+(a.expectedRemote||'分支尚不存在，将新建');
  get('personal-push-grant').disabled=false;
 }catch(e){get('personal-push-account').textContent=e.message;}
 finally{personalBusy=false;}
};
get('personal-push-grant').onclick=async()=>{
 if(personalBusy||!pushAccount||personalPending?.action!=='git.push')return;
 personalBusy=true;get('personal-push-grant').disabled=true;
 try{await personalPost({action:'push-grant',id:personalPending.id,githubId:pushAccount.id});personalDialog.close();}
 catch(e){get('personal-push-account').textContent=e.message;pushAccount=null;}
 finally{personalBusy=false;}
};
get('push-history').onclick=async()=>{
 try{const items=await personalPost({action:'push-history'});
 get('push-history-result').textContent=items.length?items.map(r=>r.input.repository+' · '+r.input.branch+'\n'+r.input.commit+'\n账号：'+r.account.login+' · '+({completed:'已完成',unknown:'待核实',rejected:'已拒绝'}[r.state]||r.state)+(r.url?'\n'+r.url:'')+(r.error?'\n'+r.error:'')).join('\n\n'):'暂无本机推送回执';
 }catch(e){get('push-history-result').textContent=e.message;}
};

get('resource-document-history').onclick=async()=>{
 const el=get('resource-document-receipts');el.hidden=false;el.textContent='正在读取本机回执…';
 try { const items=await personalPost({action:'document-history'});el.textContent=items.length?items.map(r=>[r.createdAt,r.state==='completed'?(r.contentSHA256?'正文已核验':'旧版完成回执，正文未核验'):'待核实，请检查文档，勿重复写入',r.title,r.url||r.target||'未取得链接','请求：'+r.requestId].join('\n')).join('\n\n'):'暂无本机文档回执'; }
 catch(e){el.textContent=e.message;}
};
