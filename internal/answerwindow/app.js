import {groupMessages} from './group.mjs';
import {renderMarkdown} from './markdown.mjs';
const container = document.getElementById('answers');
const statusNode = document.getElementById('status');
let revision = '';
let transcript=new Map(), hasMore=false, historyBusy=false, synced=false;
const historyButton=document.getElementById('load-history');
let room = '';
const form = document.getElementById('composer');
const input = document.getElementById('message');
const sendButton = document.getElementById('send');
const sendStatus = document.getElementById('send-status');
const stopButton=document.getElementById("stop-model");
let stopping=false, stopAttempt=null;
let sending = false;
let attachments=[], uploading=false;
const fileInput=document.getElementById("files");
let available = false;
let attempt = null;
let restored = false;
const draftKey = 'agent-room-draft:' + location.pathname;
function saveDraft() {
  try { sessionStorage.setItem(draftKey, JSON.stringify({text:input.value, attempt, attachments:attachments.filter(a=>a.path).map(({name,path,size})=>({name,path,size}))})); } catch { /* Storage is optional; in-memory retry IDs remain valid. */ }
}
input.addEventListener('input', saveDraft);
form.addEventListener('submit', async event => {
  event.preventDefault();
  const text = [input.value.trim(), attachments.length ? "附件已上传到 host，请根据需要读取文件；图片可用图片查看工具打开：\n"+attachments.map(a=>JSON.stringify({name:a.name,path:a.path})).join("\n") : ""].filter(Boolean).join("\n\n");
  if (!text || sending || uploading || !room || !available || lastState?.connected === false) return;
  if (!attempt || attempt.text !== text) attempt = {id:crypto.randomUUID().replaceAll('-', ''), text};
  saveDraft();
  sending = true; renderAttachments();fileInput.disabled=true; input.disabled = true; sendButton.disabled = true;
  sendStatus.textContent = '发送中，等待 host 确认…';
  try {
    const response = await fetch('submit', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(attempt), signal:AbortSignal.timeout(12000)});
    if (!response.ok) throw new Error(await response.text());
    input.value = ''; attempt = null;for(const a of attachments)if(a.preview)URL.revokeObjectURL(a.preview);attachments=[];renderAttachments();saveDraft();
    sendStatus.textContent = '已接收，处理状态将在对话中更新';
  } catch (error) {
    sendStatus.textContent = error.name === 'TimeoutError' || error.name === 'TypeError' ? '未收到确认，可能已提交；再次发送会安全重试。' : error.message;
  } finally {
    sending = false;renderAttachments();fileInput.disabled=false; input.disabled = false; sendButton.disabled = !room || !available || lastState?.connected === false; input.focus();
  }
});
input.addEventListener('keydown', event => {
  if (event.key !== 'Enter' || event.isComposing || event.keyCode === 229) return;
  event.preventDefault();
  if (event.altKey) {
    const length = input.value.length - (input.selectionEnd - input.selectionStart) + 1;
    if (input.maxLength >= 0 && length > input.maxLength) return;
    input.setRangeText('\n', input.selectionStart, input.selectionEnd, 'end');
    input.dispatchEvent(new Event('input', {bubbles: true}));
  } else {
    form.requestSubmit();
  }
});
let selectedUser = null;
let lastState = null;
let grouped = [];
let memberSignature = '';
function applyFilter() {
  if (!lastState) return;
  let visible = 0;
  for (const message of grouped) {
    const element = cards.get(message.key);
    if (!element) continue;
    const show = selectedUser === null || message.owner === selectedUser;
    element.hidden = !show;
    if (show) visible++;
  }
  const member = (lastState.members || []).find(m => m.UID === selectedUser);
  document.getElementById('filter-label').textContent = selectedUser === null ? '全部成员的对话' : '正在查看 ' + (member ? member.Name : '成员 ' + selectedUser) + ' 的提问和对应回答';
  for (const button of document.querySelectorAll('.member-filter')) button.setAttribute('aria-pressed', String(button.dataset.uid === String(selectedUser)));
  document.getElementById('empty').hidden = visible > 0;
  if (!visible) document.getElementById('empty').textContent = selectedUser === null ? '等待第一条消息' : '当前保留记录中没有该成员的对话';
}
function sidebar(state) {
  document.getElementById('host-address').textContent = state.host || '';
  document.getElementById('session-id').textContent = state.session || '';
  document.getElementById('viewer-name').textContent = state.viewer || '';
  const members = state.members || [];
  document.getElementById('member-count').textContent = members.length;
  const signature = JSON.stringify(members);
  if (signature === memberSignature) return;
  memberSignature = signature;
  const list = document.getElementById('member-list');
  list.replaceChildren();
  for (const member of [{UID:null,Name:'全部成员'}, ...members]) {
    const button = document.createElement('button');
    button.type = 'button';button.className = 'member-filter';button.dataset.uid = String(member.UID);
    button.textContent = member.Name;
    button.addEventListener('click', () => {selectedUser = member.UID;applyFilter();});
    list.append(button);
  }
}
document.getElementById('copy-session').addEventListener('click', async event => {
  try {await navigator.clipboard.writeText(lastState.session);event.target.textContent='已复制';}
  catch {event.target.textContent='请选中上方文本复制';}
});
const cards = new Map();
function card(answer) {
  const article = document.createElement('article');
  const bar = document.createElement('div');bar.className = 'answer-bar';
  const label = document.createElement('span');label.className = 'message-author';
  const copy = document.createElement('button');copy.textContent = '复制';
  const record = {article, label, copy, answer, signature:'', wasFinal:false, initialized:false, hadProgress:false};
  copy.addEventListener('click', async () => {
    try {await navigator.clipboard.writeText(record.answer.text);copy.textContent = '已复制';}
    catch {copy.textContent = '复制失败，请选中文字';}
    setTimeout(() => {copy.textContent = '复制';}, 2000);
  });
  bar.append(label, copy);
  const body = document.createElement('div');body.className = 'answer-text';
  const acknowledgement = document.createElement('div');acknowledgement.className = 'acknowledgement';acknowledgement.setAttribute('role','status');
  const details = document.createElement('details');details.className = 'task-progress';
  const summary = document.createElement('summary');
  const steps = document.createElement('div');steps.className = 'progress-steps';
  details.append(summary,steps);
  article.append(bar,body,acknowledgement,details);
  Object.assign(record,{body,acknowledgement,details,summary,steps});
  article.record = record;
  updateCard(article, answer);
  return article;
}
function updateCard(article, answer) {
  const r = article.record;
  const signature = JSON.stringify(answer);
  r.answer = answer;
  if (r.signature === signature) return;
  r.signature = signature;
  article.className = answer.role === 'user' ? 'user-message' : 'model-message task-message';
  r.label.textContent = (answer.author || '成员') + (answer.kind === 'note' ? ' · 笔记' : answer.kind === 'steer' ? ' · 补充要求' : answer.role === 'assistant' ? (answer.working ? ' · 处理中' : answer.final ? ' · 回答' : ' · 任务状态') : '');
  article.classList.toggle('is-working', Boolean(answer.working));
  if (answer.role === 'assistant') renderMarkdown(r.body,answer.text); else r.body.textContent = answer.text;
  r.acknowledgement.hidden = answer.role !== 'user' || !answer.ack;
  r.acknowledgement.textContent = '模型助手 · 自动状态：' + (answer.ack || '已接收');
  const progress = answer.progress || [];
  r.details.hidden = progress.length === 0;
  r.summary.textContent = '查看执行过程 · ' + progress.length + ' 条进度';
  r.steps.replaceChildren();
  for (const text of progress) {const step=document.createElement('p');step.textContent=text;r.steps.append(step);}
  if (answer.final && !r.wasFinal) r.details.open = false;
  else if ((!r.initialized || (!r.hadProgress && progress.length > 0)) && !answer.final && answer.working) r.details.open = true;
  r.hadProgress = progress.length > 0;
  r.initialized = true;
  r.wasFinal = Boolean(answer.final);
}
function renderState(state, prepend=false) {
    const nearEnd = window.innerHeight + window.scrollY >= document.body.scrollHeight - 160;
    for (const a of state.answers || []) transcript.set(a.seq,a);
    const answers = [...transcript.values()].sort((a,b)=>a.seq-b.seq);
    grouped = groupMessages(answers);
    const retained = new Set(grouped.map(a => a.key));
    for (const [seq, element] of cards) {
      if (!retained.has(seq)) { element.remove(); cards.delete(seq); }
    }
    let previous = null;
    for (const answer of grouped) {
      let element = cards.get(answer.key);
      if (!element) {element = card(answer);cards.set(answer.key, element);}
      else updateCard(element, answer);
      const expected = previous ? previous.nextElementSibling : container.firstElementChild;
      if (element !== expected) container.insertBefore(element, expected);
      previous = element;
    }
    lastState = state;
    sidebar(state);
    applyFilter();
    revision = state.revision;
    statusNode.textContent = state.status;
    stopButton.disabled=stopping || !available || !state.connected || !state.activeTurn;
    if(stopAttempt && stopAttempt.expectedTurn!==state.activeTurn){stopAttempt=null;stopButton.textContent='停止模型';}
    sendButton.disabled = sending || uploading || !room || state.connected === false;
    document.getElementById('client-version').textContent = '本机客户端 ' + (state.clientVersion ? 'v' + state.clientVersion : '版本未知');

    if (!historyButton.dataset.initialized) { hasMore=state.dropped;historyButton.dataset.initialized='yes'; }
    historyButton.hidden=!hasMore;
    document.getElementById('notice').hidden = true;
    document.body.classList.toggle('has-answers', answers.length > 0);
    if (!prepend && nearEnd && answers.length) window.scrollTo({top: document.body.scrollHeight, behavior: 'smooth'});
}
async function poll() {
  try {
    const response = await fetch('answers?revision=' + encodeURIComponent(revision), {cache: 'no-store'});
    if (response.status === 204) {available = true; return;}
    if (!response.ok) throw new Error('unavailable');
    const state = await response.json();
    available = true;
    if (!restored) {
      restored = true;
      try {const saved = JSON.parse(sessionStorage.getItem(draftKey)); if (saved) {input.value = saved.text || ''; attempt = saved.attempt || null;attachments=saved.attachments || [];renderAttachments();}} catch { /* Ignore unavailable storage. */ }
    }
    if (room !== state.room) {
      container.replaceChildren();
      cards.clear();transcript.clear();hasMore=false;synced=false;
      selectedUser = null;
      memberSignature = '';
      room = state.room;delete historyButton.dataset.initialized;
      sendButton.disabled = sending || uploading || !room || state.connected === false;
    }
    if (!synced) {transcript.clear();delete historyButton.dataset.initialized;if(state.connected)synced=true;}
    if (transcript.size && state.answers?.length) {
      const newest=[...transcript.keys()].reduce((a,b)=>Math.max(a,b),0);let batch=state.answers;
      while(batch.length && batch[0].seq>newest && state.dropped) {
        const response=await fetch('history?before='+batch[0].seq,{cache:'no-store'});
        if(!response.ok)throw new Error('history');const gap=await response.json();
        if(gap.room!==state.room)break;
        batch=gap.answers || [];for(const a of batch)transcript.set(a.seq,a);
        if(!gap.hasMore)break;
      }
    }
    renderState(state);
  } catch {
    available = false; sendButton.disabled = true;stopButton.disabled=true;
    statusNode.textContent = '无法连接本机客户端 · 请重新运行 answers/join --answers 并打开新的 Browser URL';
    revision = '';
  } finally {
    setTimeout(poll, 1000);
  }
}
poll();

async function loadOlder() {
 if(historyBusy || !hasMore || !transcript.size)return;
 historyBusy=true;historyButton.disabled=true;historyButton.textContent='加载中…';
 const expectedRoom=room;
 const anchor=[...container.children].find(el=>el.getBoundingClientRect().bottom>0);
 const top=anchor?.getBoundingClientRect().top;
 try {
  const before=[...transcript.keys()].reduce((a,b)=>Math.min(a,b),Infinity);
  const response=await fetch('history?before='+before,{cache:'no-store',signal:AbortSignal.timeout(10000)});
  if(!response.ok)throw new Error('历史加载失败，点击重试');
  const state=await response.json();if(room!==expectedRoom || state.room!==room)return;
  for(const a of state.answers || [])transcript.set(a.seq,a);
  hasMore=state.hasMore;renderState(lastState,true);
  if(anchor?.isConnected)window.scrollBy(0,anchor.getBoundingClientRect().top-top);
 }catch(error){historyButton.textContent=error.message;}
 finally{historyBusy=false;historyButton.disabled=false;if(historyButton.textContent==='加载中…')historyButton.textContent='加载更早的消息';}
}
historyButton.addEventListener('click',loadOlder);
let previousScroll=window.scrollY;
window.addEventListener('scroll',()=>{const current=window.scrollY;if(current<previousScroll && current<150)loadOlder();previousScroll=current;},{passive:true});

function renderAttachments() {
 const list=document.getElementById('attachments');list.replaceChildren();
 for(const a of attachments) {
  const item=document.createElement('div');item.className='attachment';
  if(a.preview){const img=document.createElement('img');img.src=a.preview;img.alt=a.name;item.append(img);}
  const label=document.createElement('span');label.textContent=a.name+' · '+Math.ceil(a.size/1024)+' KB';
  const remove=document.createElement('button');remove.type='button';remove.textContent='移除';remove.disabled=sending || uploading;
  remove.addEventListener('click',()=>{attachments=attachments.filter(x=>x!==a);if(a.preview)URL.revokeObjectURL(a.preview);attempt=null;renderAttachments();saveDraft();});
  item.append(label,remove);list.append(item);
 }
}
async function uploadFiles(files) {
 if(uploading || sending || !available || lastState?.connected===false)return;
 if(attachments.length+files.length>10){sendStatus.textContent='每条消息最多 10 个附件';return;}
 uploading=true;fileInput.disabled=true;sendButton.disabled=true;
 try {
  for(const file of files) {
   if(file.size>20*1024*1024)throw new Error(file.name+' 超过 20 MiB');
   sendStatus.textContent='正在上传 '+file.name+'…';
   const data=new FormData();data.append('file',file);
   const response=await fetch('upload',{method:'POST',body:data,signal:AbortSignal.timeout(65000)});
   if(!response.ok)throw new Error(await response.text());
   const saved=await response.json();
   if(/^image\/(png|jpeg|gif|webp)$/.test(file.type))saved.preview=URL.createObjectURL(file);
   attachments.push(saved);attempt=null;renderAttachments();saveDraft();
  }
  sendStatus.textContent='附件已上传，点击发送提交给模型';
 }catch(error){sendStatus.textContent=error.message;}
 finally{uploading=false;fileInput.disabled=false;fileInput.value='';sendButton.disabled=!available || lastState?.connected===false;renderAttachments();}
}
fileInput.addEventListener('change',()=>uploadFiles([...fileInput.files]));
input.addEventListener('paste',event=>{const files=[...event.clipboardData.files];if(files.length){event.preventDefault();uploadFiles(files);}});
form.addEventListener('dragover',event=>{event.preventDefault();});
form.addEventListener('drop',event=>{event.preventDefault();uploadFiles([...event.dataTransfer.files]);});

stopButton.addEventListener('click',async()=>{
 const target=lastState?.activeTurn;
 if(stopping || !available || !lastState?.connected || !target)return;
 if(!stopAttempt || stopAttempt.expectedTurn!==target)stopAttempt={id:crypto.randomUUID().replaceAll('-',''),expectedTurn:target};
 stopping=true;stopButton.disabled=true;stopButton.textContent='正在请求停止…';
 try {
  const response=await fetch('cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(stopAttempt),signal:AbortSignal.timeout(12000)});
  if(!response.ok)throw new Error(await response.text());
  stopButton.textContent='已请求停止';sendStatus.textContent='停止请求已接收，等待模型结束当前任务。';
 }catch(error){stopButton.textContent='重试停止';sendStatus.textContent=error.message;}
 finally{stopping=false;stopButton.disabled=!available || !lastState?.connected || !lastState?.activeTurn;}
});
