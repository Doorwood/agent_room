const {test,expect}=require('@playwright/test');
const {spawn}=require('node:child_process');
const path=require('node:path');
let fixture,url,hostWebURL;
test.beforeAll(async()=>{
 fixture=spawn(path.resolve('dist/dashboard-ui-fixture'),[],{stdio:['ignore','pipe','pipe']});
 fixture.stderr.on('data',b=>{const match=b.toString().match(/HOST_WEB_URL=(http:\/\/[^\s]+)/);if(match)hostWebURL=match[1];});
 url=await new Promise((resolve,reject)=>{let text='';fixture.stdout.on('data',b=>{text+=b;if(text.includes('\n'))resolve(text.trim())});fixture.once('error',reject);fixture.once('exit',code=>reject(new Error('fixture exited '+code)));});
});
test.afterAll(async()=>{if(fixture && fixture.exitCode===null){fixture.kill('SIGTERM');await new Promise(resolve=>fixture.once('exit',resolve));}});
test('dashboard connects, opens a safe chat, sends, disconnects and reconnects',async({page,context})=>{
 await context.grantPermissions(['clipboard-read','clipboard-write']);
 const errors=[];page.on('pageerror',error=>errors.push(error.message));
 await page.goto(url);
 await expect(page.getByRole('heading',{name:'我的 Rooms'})).toBeVisible();
 await expect(page.locator('#version')).toHaveText('本机客户端 v1.0.9');
 await page.getByRole('button',{name:'连接',exact:true}).click();
 await expect(page.locator('.status')).toHaveText('已连接',{timeout:10000});
 await expect(page.locator('.room h3')).toHaveText('demo-project');
 await page.getByText('Room 信息',{exact:true}).click();
 await expect(page.locator('.details-text')).toContainText('项目名称：demo-project');
 await expect(page.locator('.details-text')).toContainText('项目路径：/workspace/demo-project');
 const popupPromise=page.waitForEvent('popup');await page.getByRole('link',{name:'打开对话 ↗'}).click();const chat=await popupPromise;
 await expect(chat.locator('#client-version')).toHaveText('本机客户端 v1.0.9');
 await expect(chat.locator('#project-name')).toHaveText('demo-project');
 await expect(chat.locator('#header-project-name')).toHaveText('demo-project');
 await expect(chat).toHaveTitle('demo-project · agent_room');
 await expect(chat.locator('.answer-text h2')).toHaveText('已准备好');
 await expect(chat.locator('.answer-text table')).toBeVisible();
 await expect(chat.locator('.answer-text img')).toHaveCount(0);
 await expect(chat.locator('.answer-text a[href^="javascript:"]')).toHaveCount(0);
 expect(await chat.evaluate(()=>window.pwned)).toBeUndefined();
 await context.route('https://example.com/**',route=>route.fulfill({contentType:'text/html',body:'<title>Link destination</title><p>Opened</p>'}));
 for(const [label,path] of [['文档','docs'],['https://example.com/plain','plain'],['https://example.com/inline','inline']]) {
  const link=chat.getByRole('link',{name:label,exact:true});
  await expect(link).toHaveAttribute('target','_blank');await expect(link).toHaveAttribute('rel','noopener noreferrer');
  const opened=chat.waitForEvent('popup');await link.click();const destination=await opened;
  await expect(destination).toHaveURL('https://example.com/'+path);expect(await destination.evaluate(()=>window.opener===null)).toBe(true);await destination.close();
 }

 await chat.getByRole('button',{name:'复制代码',exact:true}).click();
 expect(await chat.evaluate(()=>navigator.clipboard.readText())).toBe('agent_room dashboard');
 const input=chat.locator('#message');await input.fill('第一行');await input.press('Alt+Enter');await input.type('第二行');await expect(input).toHaveValue('第一行\n第二行');
 await input.dispatchEvent('keydown',{key:'Enter',isComposing:true});await expect(input).toHaveValue('第一行\n第二行');
 await input.press('Enter');await expect(input).toHaveValue('');await expect(chat.locator('.user-message .answer-text').last()).toHaveText('第一行\n第二行');
 await expect(chat.locator('#stop-model')).toBeDisabled();
 await input.fill('测试长任务');await input.press('Enter');await expect(chat.locator('#stop-model')).toBeEnabled();
 await chat.locator('#stop-model').click();await expect(chat.locator('#stop-model')).toBeDisabled();
 await expect(chat.locator('.user-message .acknowledgement').last()).toContainText('任务已中断');
 await expect(page.locator('.status')).toHaveText('已连接');
 await page.screenshot({path:'dist/dashboard-desktop.png' ,fullPage:true});
 await chat.screenshot({path:'dist/chat-markdown.png',fullPage:true});
 await page.getByRole('button',{name:'断开连接'}).click();await expect(page.locator('.status')).toHaveText('未连接',{timeout:10000});await expect(page.getByRole('link',{name:'打开对话 ↗'})).toHaveCount(0);
 await page.reload();await expect(page.locator('.room h3')).toHaveText('demo-project');
 await page.locator('#search').fill('demo-project');await expect(page.locator('#rooms article')).toHaveCount(1);await page.locator('#search').fill('');
 await page.getByRole('button',{name:'连接',exact:true}).click();await expect(page.locator('.status')).toHaveText('已连接',{timeout:10000});
 await page.setViewportSize({width:390,height:844});await page.screenshot({path:'dist/dashboard-mobile.png',fullPage:true});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
 expect(errors).toEqual([]);
 await page.getByRole('button',{name:'断开连接'}).click();await expect(page.locator('.status')).toHaveText('未连接');
});
test('add validates identities and refresh preserves room list',async({page})=>{
 await page.goto(url);await page.locator('#address').fill('127.0.0.1:7444');await page.locator('#session').fill('invalid');await page.locator('#name').fill('bob');await page.locator('#join').click();await expect(page.locator('#join-status')).toContainText('complete session_id');
 await page.locator('#session').fill('c'.repeat(32)+'.'+'d'.repeat(64));await page.locator('#join').click();await expect(page.locator('#rooms article')).toHaveCount(2);await page.reload();await expect(page.locator('#rooms article')).toHaveCount(2);
 await page.locator('#search').fill('7444');await expect(page.locator('#rooms article')).toHaveCount(1);await expect(page.locator('#rooms')).toContainText('bob');
});

test('history loads upwards, attachments submit, and room deletion persists',async({page})=>{
 await page.goto(url);
 await page.locator('#address').fill('127.0.0.1:7450');await page.locator('#session').fill('e'.repeat(32)+'.'+'f'.repeat(64));await page.locator('#name').fill('history');await page.locator('#join').click();
 const entry=page.locator('.room').filter({hasText:'127.0.0.1:7450'});await expect(entry.locator('.status')).toHaveText('已连接');
 const popup=page.waitForEvent('popup');await entry.getByRole('link',{name:'打开对话 ↗'}).click();const chat=await popup;
 await expect(chat.locator('.user-message')).toHaveCount(50);
 await expect(chat.getByText('一起看看项目的下一步',{exact:true})).toHaveCount(0);
 await chat.evaluate(()=>window.scrollTo(0,0));
 for(let i=0;i<4;i++) {const load=chat.locator('#load-history');if(await load.isVisible())await load.click();await chat.waitForTimeout(150);}
 await expect(chat.getByText('一起看看项目的下一步',{exact:true})).toBeVisible();
 await expect(chat.locator('.user-message')).toHaveCount(131);
 // View navigation remains reachable at the bottom of a long transcript,
 // including below main where the sticky composer and footer live.
 for(const width of [1280,390,320]) {
  await chat.setViewportSize({width,height:844});
  await chat.evaluate(()=>window.scrollTo(0,document.body.scrollHeight));
  const nav=chat.locator('#conversation-views');
  await expect.poll(async()=>{const box=await nav.boundingBox();return box && box.y>=0 && box.y+box.height<=844;}).toBe(true);
  expect(await chat.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await chat.locator('#task-view').click();await expect(chat.locator('#tasks-panel')).toBeVisible();
  await chat.locator('#question-view').click();await expect(chat.locator('#questions-panel')).toBeVisible();
  await chat.locator('#main-view').click();await expect(chat.locator('#answers')).toBeVisible();
 }
 await chat.evaluate(()=>window.scrollTo(0,document.body.scrollHeight));
 await chat.screenshot({path:'dist/sticky-navigation-mobile.png'});
 await chat.setViewportSize({width:1280,height:900});
 await chat.evaluate(()=>window.scrollTo(0,document.body.scrollHeight));
 await chat.screenshot({path:'dist/sticky-navigation-desktop.png'});

 await chat.locator('#files').setInputFiles([
  {name:'notes.txt',mimeType:'text/plain',buffer:Buffer.from('attached notes')},
  {name:'pixel.png',mimeType:'image/png',buffer:Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZfoAAAAASUVORK5CYII=','base64')}
 ]);
 await expect(chat.locator('#attachments .attachment')).toHaveCount(2);await expect(chat.locator('#attachments img')).toHaveCount(1);
 await expect(chat.locator('#send-status')).toHaveText('附件已上传，点击发送提交给模型');
 await chat.locator('#message').fill('附件草稿');
 await chat.reload();await expect(chat.locator('#message')).toHaveValue('附件草稿');await expect(chat.locator('#attachments img')).toHaveCount(1);
 await expect(chat.locator('#attachments img')).toHaveJSProperty('complete',true);
 await chat.locator('#send').click();await expect(chat.locator('#attachments .attachment')).toHaveCount(0);
 await expect(chat.locator('.user-message .answer-text').last()).toContainText('notes.txt');await expect(chat.locator('.user-message .answer-text').last()).toContainText('pixel.png');
 await expect(chat.locator('.user-message').last().locator('a.sent-attachment')).toHaveCount(2);
 const downloaded=chat.waitForEvent('download');await chat.locator('.user-message').last().getByRole('link',{name:'notes.txt',exact:true}).click();const file=await downloaded;expect(file.suggestedFilename()).toBe('notes.txt');
 await chat.locator('#message').fill('跨重连草稿');await chat.waitForResponse(r=>r.url().endsWith('/draft') && r.request().method()==='POST');
 await entry.getByRole('button',{name:'断开连接'}).click();await entry.getByRole('button',{name:'连接',exact:true}).click();await entry.getByRole('link',{name:'打开对话 ↗'}).waitFor();const reopened=page.waitForEvent('popup');await entry.getByRole('link',{name:'打开对话 ↗'}).click();const next=await reopened;await expect(next.locator('#message')).toHaveValue('跨重连草稿');
 page.once('dialog',dialog=>dialog.accept());await entry.getByRole('button',{name:'删除 Room'}).click();await expect(entry).toHaveCount(0);await page.reload();await expect(entry).toHaveCount(0);
});

test('visitor searches read-only history and asker uses an independent area',async({page})=>{
 await page.goto(url);
 async function join(name,port){await page.locator('#address').fill('127.0.0.1:'+port);await page.locator('#session').fill('1'.repeat(32)+'.'+'2'.repeat(64));await page.locator('#name').fill(name);await page.locator('#join').click();const entry=page.locator('.room').filter({hasText:'127.0.0.1:'+port});await entry.getByRole('link',{name:'打开对话 ↗'}).waitFor();const opened=page.waitForEvent('popup');await entry.getByRole('link',{name:'打开对话 ↗'}).click();return opened;}
 const visitor=await join('visitor',7460);await expect(visitor.locator('#role-label')).toContainText('参观者');await expect(visitor.locator('#composer')).toBeHidden();await expect(visitor.locator('#questions-panel')).toBeHidden();await visitor.locator('#history-query').fill('下一步');await visitor.locator('#history-search').getByRole('button',{name:'搜索',exact:true}).click();await expect(visitor.locator('#search-results')).toContainText('一起看看项目的下一步');
 expect(await visitor.evaluate(async()=>{const r=await fetch('submit',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:'a'.repeat(32),text:'run work'})});return r.status})).toBe(403);
 await visitor.locator('#task-view').click();await expect(visitor.locator('#tasks-panel')).toBeVisible();expect(await visitor.evaluate(async()=>{const r=await fetch('tasks',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'convert',sourceSeq:1,title:'forged',requestId:'a'.repeat(32)})});return r.status})).toBe(403);await expect(visitor.locator('.convert-task:visible')).toHaveCount(0);
 const asker=await join('asker',7461);await expect(asker.locator('#role-label')).toContainText('询问者');await expect(asker.locator('#composer')).toBeHidden();await asker.locator('#question-view').click();const before=await asker.locator('.user-message').count();await asker.locator('#ask-text').fill('独立问题：解释事务');await asker.locator('#ask-send').click();await expect(asker.locator('#questions-list')).toContainText('只读回答：独立问题：解释事务');await expect(asker.locator('.user-message')).toHaveCount(before);
 const member=await join('observer',7462);await member.locator('#question-view').click();await expect(member.locator('#questions-list')).toContainText('独立问题：解释事务');await expect(member.locator('#ask-form')).toBeHidden();await member.locator('#question-member').selectOption('1001');await expect(member.locator('#questions-list')).toContainText('暂无问答记录');await member.locator('#question-member').selectOption('1002');await expect(member.locator('#questions-list')).toContainText('解释事务');await expect(member.locator('#answers')).toBeHidden();await member.locator('#main-view').click();await expect(member.locator('#questions-panel')).toBeHidden();await expect(member.locator('#answers')).toBeVisible();
});

test('project tasks convert without replay, complete, reopen, continue and persist',async({page})=>{
 await page.goto(url);await page.locator('#address').fill('127.0.0.1:7470');await page.locator('#session').fill('3'.repeat(32)+'.'+'4'.repeat(64));await page.locator('#name').fill('tasks');await page.locator('#join').click();
 const entry=page.locator('.room').filter({hasText:'127.0.0.1:7470'});await entry.getByRole('link',{name:'打开对话 ↗'}).waitFor();const popup=page.waitForEvent('popup');await entry.getByRole('link',{name:'打开对话 ↗'}).click();const chat=await popup;
 const errors=[];chat.on('pageerror',e=>errors.push(e.message));
 await expect(chat.locator('.user-message .message-time')).toHaveText(/\d{4}\/\d{2}\/\d{2}.*\d{2}:\d{2}:\d{2}/);
 await expect(chat.locator('.model-message .message-time')).toHaveText(/\d{4}\/\d{2}\/\d{2}/);
 const initial=await chat.locator('.user-message').count();
 await chat.locator('.user-message .convert-task').click();await chat.locator('#task-title').fill('登录优化');await chat.locator('#task-acceptance').fill('登录正常，相关检查通过');await chat.locator('#create-task').getByRole('button',{name:'创建任务',exact:true}).click();
 await expect(chat.locator('#task-heading')).toHaveText('T-1 · 登录优化');await expect(chat.locator('#task-state')).toHaveText('待验收');await expect(chat.locator('#task-runs')).toContainText('实现完成，请验收。');await expect(chat.locator('.user-message')).toHaveCount(initial);await expect(chat.locator('#answers')).toBeHidden();
 await chat.getByRole('button',{name:'标记任务完成',exact:true}).click();await expect(chat.locator('#task-state')).toHaveText('已完成');await expect(chat.locator('#task-send')).toBeDisabled();
 await chat.reload();await chat.locator('#task-view').click();await chat.locator('.task-list-item').click();await expect(chat.locator('#task-state')).toHaveText('已完成');
 await chat.getByRole('button',{name:'重新打开任务',exact:true}).click();await expect(chat.locator('#task-state')).toHaveText('待验收');
 await chat.locator('#task-text').fill('补充登录测试');await chat.waitForResponse(r=>r.url().endsWith('/draft')&&r.request().method()==='POST');
 await chat.reload();await chat.locator('#task-view').click();await chat.locator('.task-list-item').click();await expect(chat.locator('#task-text')).toHaveValue('补充登录测试');
 await chat.locator('#main-view').click();await expect(chat.locator('#answers')).toBeVisible();await expect(chat.locator('#message')).toHaveValue('');await chat.locator('#task-view').click();await expect(chat.locator('#task-text')).toHaveValue('补充登录测试');
 await chat.locator('#task-send').click();await expect(chat.locator('#task-text')).toHaveValue('');await expect(chat.locator('#task-runs article')).toHaveCount(2);await expect(chat.locator('#task-runs')).toContainText('补充登录测试');
 await chat.getByRole('button',{name:'标记任务完成',exact:true}).click();await expect(chat.locator('#task-state')).toHaveText('已完成');
 await chat.locator('#main-view').click();await expect(chat.locator('.user-message')).toHaveCount(initial+1);await chat.locator('.user-message .convert-task').last().click();await chat.locator('#create-task').getByRole('button',{name:'创建任务',exact:true}).click();await expect(chat.locator('.task-list-item')).toHaveCount(1);await expect(chat.locator('#task-state')).toHaveText('已完成');await expect(chat.locator('#task-runs article')).toHaveCount(2);
 await chat.setViewportSize({width:390,height:844});await chat.screenshot({path:'dist/project-tasks-mobile.png',fullPage:true});expect(await chat.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
 expect(errors).toEqual([]);
});


test('host welcome page is read only and detects a running local dashboard',async({page})=>{
 await expect.poll(()=>hostWebURL).toBeTruthy();await page.goto(hostWebURL);
 await expect(page.locator('#project')).toHaveText('demo-project');await expect(page.locator('#join')).toContainText('agent_room answers');
 await expect(page.locator('#install')).toContainText('agent_room-setup');
 expect(await page.evaluate(async()=>{const r=await fetch('/connect',{method:'POST',body:'{}'});return r.status;})).toBe(405);
 expect(await page.evaluate(async()=>{const r=await fetch('/rooms');return r.status;})).toBe(404);
 await page.evaluate(()=>{window.receivedCheck=null;addEventListener('message',e=>{if(e.data?.app==='agent_room')window.receivedCheck=e.data;});});
 const opened=page.waitForEvent('popup');await page.locator('#detect').click();const check=await opened;
 await expect(page.locator('#detect-status')).toContainText('已检测到本机 Dashboard');
 await expect(check.locator('#dashboard')).toBeVisible();
 const payload=await page.evaluate(()=>window.receivedCheck);expect(Object.keys(payload).sort()).toEqual(['app','nonce','version']);
 await check.close();
 await page.setViewportSize({width:390,height:844});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.screenshot({path:'dist/host-welcome-mobile.png',fullPage:true});
});

test('unavailable client detection stays unknown instead of claiming not installed',async({page,context})=>{
 await context.route('http://127.0.0.1:18743/**',route=>route.abort());
 await page.goto(hostWebURL);const opened=page.waitForEvent('popup');await page.locator('#detect').click();const check=await opened;
 await page.evaluate(()=>postMessage({app:'agent_room',version:'forged',nonce:'0'.repeat(32)},location.origin));
 await expect(page.locator('#detect-status')).toContainText('未检测到运行中的 Dashboard',{timeout:8000});
 await expect(page.locator('#detect-status')).toContainText('尚未启动');await expect(page.locator('#detect')).toBeEnabled();await check.close();
});

test('welcome application requires local confirmation and continues in dashboard',async({page,context})=>{
 await page.goto(hostWebURL);
 await page.locator('#join-name').fill('web-user');
 const opened=page.waitForEvent('popup');await page.locator('#apply').click();const local=await opened;
 await expect(page.locator('#apply-status')).toContainText('尚未提交申请');
 await expect(local.locator('#join-target')).toContainText('web-user');
 const dashboard=await context.newPage();await dashboard.goto(url);
 await expect(dashboard.locator('.room').filter({hasText:'web-user'})).toHaveCount(0);
 await page.evaluate(()=>{window.joinMessages=[];addEventListener('message',e=>{if(e.data?.state)window.joinMessages.push(e.data);});});
 await local.locator('#confirm-join').click();
 await expect(page.locator('#apply-status')).toContainText('已获批准并连接',{timeout:10000});
 await dashboard.reload();await expect(dashboard.locator('.room').filter({hasText:'web-user'})).toHaveCount(1);
 const messages=await page.evaluate(()=>window.joinMessages);
 expect(messages.length).toBeGreaterThan(0);
 for(const message of messages)expect(Object.keys(message).sort()).toEqual(['app','nonce','state','version']);
 await local.close();
 const reopened=page.waitForEvent('popup');await page.locator('#apply').click();const again=await reopened;
 await again.locator('#confirm-join').click();await expect(page.locator('#apply-status')).toContainText('已获批准并连接');
 await dashboard.reload();await expect(dashboard.locator('.room').filter({hasText:'web-user'})).toHaveCount(1);
 await again.close();
});

test('personal resources require consent, stay private, revoke and share only as draft',async({page})=>{
 const fs=require('node:fs'),os=require('node:os');
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'agent-room-resource-ui-'));
 fs.writeFileSync(path.join(root,'README.md'),'Only this member can see the resource fixture.');
 try{
  await page.goto(url);await page.locator('#address').fill('127.0.0.1:7480');
  await page.locator('#session').fill('5'.repeat(32)+'.'+'6'.repeat(64));await page.locator('#name').fill('resource-user');await page.locator('#join').click();
  const entry=page.locator('.room').filter({hasText:'127.0.0.1:7480'});
  await entry.getByRole('link',{name:'打开对话 ↗'}).waitFor();
  const opened=page.waitForEvent('popup');await entry.getByRole('link',{name:'打开对话 ↗'}).click();const chat=await opened;
  const errors=[];chat.on('pageerror',e=>errors.push(e.message));
  async function decide(selector,accepted=true){
   const handled=new Promise(resolve=>chat.once('dialog',async d=>{if(accepted)await d.accept();else await d.dismiss();resolve();}));
   await chat.locator(selector).click();await handled;
  }
  await expect(chat.locator('.user-message').first()).toBeVisible();
  const initial=await chat.locator('.user-message').count();
  await chat.locator('#resource-open').click();await expect(chat.locator('#resource-info')).toContainText('个人授权');
  await chat.locator('#resource-action').selectOption('project.file');await chat.locator('#resource-target').fill('README.md');
  await chat.locator('#resource-question').fill('解释这个文件');
  await decide('#resource-submit');
  await expect(chat.locator('#resource-status')).toContainText('请先开启个人授权');
  await chat.locator('#resource-root').fill(root);await chat.locator('#resource-enable').click();
  await expect(chat.locator('#resource-info')).toContainText('本机授权已开启');
  await decide('#resource-submit',false);await expect(chat.locator('#resource-submit')).toBeEnabled();await expect(chat.locator('#resource-result')).toBeHidden();
  await decide('#resource-submit');
  await expect(chat.locator('#resource-answer')).toContainText('Only this member');
  await expect(chat.locator('.user-message')).toHaveCount(initial);
  await expect(chat.locator('#message')).toHaveValue('');
  await chat.setViewportSize({width:390,height:844});
  expect(await chat.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await chat.screenshot({path:'dist/personal-resources-mobile.png'});
  await decide('#resource-share');
  await expect(chat.locator('#message')).toHaveValue(/明确分享的资源内容/);
  await expect(chat.locator('.user-message')).toHaveCount(initial);
  await chat.locator('#resource-open').click();await chat.locator('#resource-revoke').click();
  await expect(chat.locator('#resource-info')).toContainText('本机授权未开启');
  await decide('#resource-submit');
  await expect(chat.locator('#resource-status')).toContainText('请先开启个人授权');
  expect(errors).toEqual([]);
  await chat.close();
 }finally{fs.rmSync(root,{recursive:true,force:true});}
});

test('personal document creation confirms local account and content without publishing to chat',async({page})=>{
 await page.goto(url);await page.locator('#address').fill('127.0.0.1:7481');
 await page.locator('#session').fill('7'.repeat(32)+'.'+'8'.repeat(64));await page.locator('#name').fill('document-user');await page.locator('#join').click();
 const entry=page.locator('.room').filter({hasText:'127.0.0.1:7481'});
 const opened=page.waitForEvent('popup');await entry.getByRole('link',{name:'打开对话 ↗'}).click();const chat=await opened;
 const errors=[],writes=[];chat.on('pageerror',e=>errors.push(e.message));
 await chat.route('**/resources',async route=>{
  const r=route.request();if(r.method()==='POST'){
   const b=r.postDataJSON();
   if(b.action==='feishu-identity'){await route.fulfill({contentType:'application/json',body:JSON.stringify({name:'Browser User',openId:'ou_browser123456'})});return;}
   if(b.action==='query'&&b.resource.action==='feishu.create')writes.push(b);
  }
  await route.continue();
 });
 await expect(chat.locator('.create-document:visible').first()).toBeVisible();
 const initial=await chat.locator('.user-message').count();
 await chat.locator('.create-document:visible').first().click();
 await expect(chat.locator('#resource-create-fields')).toBeVisible();
 await expect(chat.locator('#resource-content')).not.toHaveValue('');
 await chat.locator('#resource-title').fill('我的项目设计');
 await chat.locator('#resource-enable').click();await expect(chat.locator('#resource-info')).toContainText('本机授权已开启');
 async function confirmCreation(accept){
  const handled=new Promise(resolve=>chat.once('dialog',async d=>{
   expect(d.message()).toContain('Browser User');expect(d.message()).toContain('ou_browser123456');expect(d.message()).toContain('我的项目设计');
   if(accept)await d.accept();else await d.dismiss();resolve();
  }));await chat.locator('#resource-submit').click();await handled;
 }
 await confirmCreation(false);await expect(chat.locator('#resource-submit')).toBeEnabled();expect(writes).toHaveLength(0);
 await confirmCreation(true);await expect(chat.locator('#resource-status')).toContainText('Browser User 的个人飞书账号创建');
 expect(writes).toHaveLength(1);expect(writes[0].resource.accountId).toBe('ou_browser123456');expect(writes[0].mode).toBe('personal');
 expect(writes[0].resource.content).toBe(await chat.locator('#resource-content').inputValue());
 await expect(chat.locator('#resource-document-link')).toHaveAttribute('target','_blank');
 await expect(chat.locator('#resource-document-link')).toHaveAttribute('href','https://example.feishu.cn/docx/BrowserDoc123');
 await expect(chat.locator('.user-message')).toHaveCount(initial);await expect(chat.locator('#message')).toHaveValue('');
 await confirmCreation(true);await expect(chat.locator('#resource-submit')).toBeEnabled();expect(writes).toHaveLength(2);
 expect(writes[0].resource.requestId).toBe(writes[1].resource.requestId);
 await chat.setViewportSize({width:390,height:844});expect(await chat.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await chat.screenshot({path:'dist/personal-create-mobile.png'});
 expect(errors).toEqual([]);await chat.close();
});

test('natural language creates as sender after local authorization then automatically on the next request',async({page})=>{
 await page.goto(url);await page.locator('#address').fill('127.0.0.1:7482');
 await page.locator('#session').fill('9'.repeat(32)+'.'+'a'.repeat(64));await page.locator('#name').fill('natural-sender');await page.locator('#join').click();
 const entry=page.locator('.room').filter({hasText:'127.0.0.1:7482'});
 const opened=page.waitForEvent('popup');await entry.getByRole('link',{name:'打开对话 ↗'}).click();const chat=await opened;
 const errors=[];chat.on('pageerror',e=>errors.push(e.message));
 await expect(chat.locator('#send')).toBeEnabled();
 await chat.locator('#message').fill('创建一个项目规划飞书文档');await chat.locator('#message').press('Enter');
 await expect(chat.locator('#personal-dialog')).toBeVisible({timeout:10000});
 await expect(chat.locator('#personal-sender')).toContainText('natural-sender');
 await expect(chat.locator('#personal-title')).toContainText('创建一个项目规划飞书文档');
 await expect(chat.locator('#personal-grant')).toBeDisabled();
 await expect(chat.locator('.answer-text a[href*="NaturalDoc"]')).toHaveCount(0);
 await chat.locator('#personal-identity').click();await expect(chat.locator('#personal-account')).toContainText('Browser User');
 await expect(chat.locator('#personal-account')).toContainText('ou_browser123456');
 await chat.setViewportSize({width:390,height:844});expect(await chat.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await chat.screenshot({path:'dist/natural-personal-authorization-mobile.png'});
 await chat.locator('#personal-grant').click();
 await expect(chat.locator('.answer-text a[href*="NaturalDoc"]')).toHaveCount(1,{timeout:10000});
 await expect(chat.locator('#personal-dialog')).toBeHidden();
 await expect(chat.locator('.answer-text').last()).toContainText('Browser User 的个人权限创建');
 await chat.locator('#message').fill('创建一个复盘飞书文档');await chat.locator('#message').press('Enter');
 await expect(chat.locator('.answer-text a[href*="NaturalDoc"]')).toHaveCount(2,{timeout:10000});
 await expect(chat.locator('#personal-dialog')).toBeHidden();
 // Revocation causes the next natural-language request to ask the sender again.
 await chat.locator('#resource-open').click();await chat.locator('#resource-revoke').click();await expect(chat.locator('#resource-info')).toContainText('本机授权未开启');await chat.locator('#resource-close').click();
 await chat.locator('#message').fill('创建一个验收飞书文档');await chat.locator('#message').press('Enter');
 await expect(chat.locator('#personal-dialog')).toBeVisible({timeout:10000});
 await chat.locator('#personal-decline').click();await expect(chat.locator('#personal-dialog')).toBeHidden();
 await expect(chat.locator('.answer-text').last()).toContainText('个人创建未完成');
 await expect(chat.locator('.answer-text a[href*="NaturalDoc"]')).toHaveCount(2);
 // A login that becomes invalid immediately after grant must ask again for the same request.
 let grantLost=false;
 await chat.route('**/personal',async route=>{
  if(route.request().method()==='POST'){grantLost=true;await route.fulfill({contentType:'application/json',body:'{}'});return;}
  await route.fulfill({contentType:'application/json',body:JSON.stringify({pending:{id:'c'.repeat(32),title:'账号失效检查',content:'模拟请求，不执行创建',sender:'natural-sender'},accountId:'',busy:false,status:grantLost?'授权后账号失效，请重新核对':'等待本机授权'})});
 });
 await expect(chat.locator('#personal-title')).toHaveText('账号失效检查');await expect(chat.locator('#personal-dialog')).toBeVisible();
 await chat.locator('#personal-identity').click();await expect(chat.locator('#personal-grant')).toBeEnabled();await chat.locator('#personal-grant').click();
 await expect(chat.locator('#personal-status')).toContainText('授权后账号失效');await expect(chat.locator('#personal-dialog')).toBeVisible();
 await expect(chat.locator('#personal-grant')).toBeDisabled();
 expect(errors).toEqual([]);await chat.close();
});

test('explicit commit asks sender for Git identity, reuses it, and never treats edit-only as commit',async({page})=>{
 await page.goto(url);await page.locator('#address').fill('127.0.0.1:7483');
 await page.locator('#session').fill('b'.repeat(32)+'.'+'c'.repeat(64));await page.locator('#name').fill('git-sender');await page.locator('#join').click();
 const entry=page.locator('.room').filter({hasText:'127.0.0.1:7483'});
 const opened=page.waitForEvent('popup');await entry.getByRole('link',{name:'打开对话 ↗'}).click();const chat=await opened;
 const errors=[];chat.on('pageerror',e=>errors.push(e.message));
 await expect(chat.locator('#send')).toBeEnabled();await chat.locator('#message').fill('优化代码');await chat.locator('#message').press('Enter');
 await expect(chat.locator('.user-message').last()).toContainText('优化代码');await expect(chat.locator('#personal-dialog')).toBeHidden();
 await chat.locator('#message').fill('优化代码并提交');await chat.locator('#message').press('Enter');
 await expect(chat.locator('#personal-dialog')).toBeVisible({timeout:10000});await expect(chat.locator('#personal-heading')).toContainText('Git 提交署名');
 await expect(chat.locator('#personal-git')).toBeVisible();await expect(chat.locator('#personal-feishu')).toBeHidden();await expect(chat.locator('#personal-content')).toContainText('code.go');
 await chat.locator('#personal-git-grant').click();await expect(chat.locator('#personal-git-status')).toContainText('请填写');
 await chat.locator('#personal-git-name').fill('Sender Alice');await chat.locator('#personal-git-email').fill('alice@example.test');
 await chat.setViewportSize({width:390,height:844});expect(await chat.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await chat.screenshot({path:'dist/personal-git-author-mobile.png'});
 await chat.locator('#personal-git-grant').click();await expect(chat.locator('.answer-text').last()).toContainText('Author/Committer：Sender Alice alice@example.test',{timeout:10000});await expect(chat.locator('#personal-dialog')).toBeHidden();
 await chat.locator('#message').fill('继续优化代码并提交');await chat.locator('#message').press('Enter');
 await expect(chat.locator('.answer-text').filter({hasText:'Author/Committer：Sender Alice'})).toHaveCount(2,{timeout:10000});await expect(chat.locator('#personal-dialog')).toBeHidden();
 await chat.locator('#resource-open').click();await chat.locator('#resource-revoke').click();await expect(chat.locator('#resource-info')).toContainText('未开启');await chat.locator('#resource-close').click();
 await chat.locator('#message').fill('再次优化代码并提交');await chat.locator('#message').press('Enter');await expect(chat.locator('#personal-dialog')).toBeVisible({timeout:10000});
 await chat.locator('#personal-decline').click();await expect(chat.locator('.answer-text').last()).toContainText('发送者未确认 Git 署名');
 await expect(chat.locator('.answer-text').filter({hasText:'Author/Committer：Sender Alice'})).toHaveCount(2);
 expect(errors).toEqual([]);await chat.close();
});
