import {groupMessages} from './group.mjs';
import {renderMarkdown} from './markdown.mjs';
const container = document.getElementById('answers');
const statusNode = document.getElementById('status');
let revision = '';
let room = '';
const form = document.getElementById('composer');
const input = document.getElementById('message');
const sendButton = document.getElementById('send');
const sendStatus = document.getElementById('send-status');
let sending = false;
let available = false;
let attempt = null;
let restored = false;
const draftKey = 'agent-room-draft:' + location.pathname;
function saveDraft() {
  try { sessionStorage.setItem(draftKey, JSON.stringify({text:input.value, attempt})); } catch { /* Storage is optional; in-memory retry IDs remain valid. */ }
}
input.addEventListener('input', saveDraft);
form.addEventListener('submit', async event => {
  event.preventDefault();
  const text = input.value.trim();
  if (!text || sending || !room || !available || lastState?.connected === false) return;
  if (!attempt || attempt.text !== text) attempt = {id:crypto.randomUUID().replaceAll('-', ''), text};
  saveDraft();
  sending = true; input.disabled = true; sendButton.disabled = true;
  sendStatus.textContent = '发送中，等待 host 确认…';
  try {
    const response = await fetch('submit', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(attempt), signal:AbortSignal.timeout(12000)});
    if (!response.ok) throw new Error(await response.text());
    input.value = ''; attempt = null; saveDraft();
    sendStatus.textContent = '已接收，处理状态将在对话中更新';
  } catch (error) {
    sendStatus.textContent = error.name === 'TimeoutError' || error.name === 'TypeError' ? '未收到确认，可能已提交；再次发送会安全重试。' : error.message;
  } finally {
    sending = false; input.disabled = false; sendButton.disabled = !room || !available || lastState?.connected === false; input.focus();
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
async function poll() {
  try {
    const response = await fetch('answers?revision=' + encodeURIComponent(revision), {cache: 'no-store'});
    if (response.status === 204) {available = true; return;}
    if (!response.ok) throw new Error('unavailable');
    const state = await response.json();
    available = true;
    if (!restored) {
      restored = true;
      try {const saved = JSON.parse(sessionStorage.getItem(draftKey)); if (saved) {input.value = saved.text || ''; attempt = saved.attempt || null;}} catch { /* Ignore unavailable storage. */ }
    }
    if (room !== state.room) {
      container.replaceChildren();
      cards.clear();
      selectedUser = null;
      memberSignature = '';
      room = state.room;
      sendButton.disabled = sending || !room || state.connected === false;
    }
    const nearEnd = window.innerHeight + window.scrollY >= document.body.scrollHeight - 160;
    const answers = state.answers || [];
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
    sendButton.disabled = sending || !room || state.connected === false;
    document.getElementById('client-version').textContent = '本机客户端 ' + (state.clientVersion ? 'v' + state.clientVersion : '版本未知');

    document.getElementById('notice').hidden = !state.dropped;
    document.body.classList.toggle('has-answers', answers.length > 0);
    if (nearEnd && answers.length) window.scrollTo({top: document.body.scrollHeight, behavior: 'smooth'});
  } catch {
    available = false; sendButton.disabled = true;
    statusNode.textContent = '无法连接本机客户端 · 请重新运行 answers/join --answers 并打开新的 Browser URL';
    revision = '';
  } finally {
    setTimeout(poll, 1000);
  }
}
poll();
