const {test,expect}=require('@playwright/test');
const {spawn}=require('node:child_process');
const path=require('node:path');
let fixture,url;
test.beforeAll(async()=>{
 fixture=spawn(path.resolve('dist/dashboard-ui-fixture'),[],{stdio:['ignore','pipe','pipe']});
 url=await new Promise((resolve,reject)=>{let text='';fixture.stdout.on('data',b=>{text+=b;if(text.includes('\n'))resolve(text.trim())});fixture.once('error',reject);fixture.once('exit',code=>reject(new Error('fixture exited '+code)));});
});
test.afterAll(async()=>{if(fixture && fixture.exitCode===null){fixture.kill('SIGTERM');await new Promise(resolve=>fixture.once('exit',resolve));}});
test('dashboard connects, opens a safe chat, sends, disconnects and reconnects',async({page,context})=>{
 await context.grantPermissions(['clipboard-read','clipboard-write']);
 const errors=[];page.on('pageerror',error=>errors.push(error.message));
 await page.goto(url);
 await expect(page.getByRole('heading',{name:'我的 Rooms'})).toBeVisible();
 await expect(page.locator('#version')).toHaveText('本机客户端 v1.0.5');
 await page.getByRole('button',{name:'连接',exact:true}).click();
 await expect(page.locator('.status')).toHaveText('已连接',{timeout:10000});
 await expect(page.locator('.room h3')).toHaveText('demo-project');
 await page.getByText('Room 信息',{exact:true}).click();
 await expect(page.locator('.details-text')).toContainText('项目名称：demo-project');
 await expect(page.locator('.details-text')).toContainText('项目路径：/workspace/demo-project');
 const popupPromise=page.waitForEvent('popup');await page.getByRole('link',{name:'打开对话 ↗'}).click();const chat=await popupPromise;
 await expect(chat.locator('#client-version')).toHaveText('本机客户端 v1.0.5');
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
 const asker=await join('asker',7461);await expect(asker.locator('#role-label')).toContainText('询问者');await expect(asker.locator('#composer')).toBeHidden();await asker.locator('#question-view').click();const before=await asker.locator('.user-message').count();await asker.locator('#ask-text').fill('独立问题：解释事务');await asker.locator('#ask-send').click();await expect(asker.locator('#questions-list')).toContainText('只读回答：独立问题：解释事务');await expect(asker.locator('.user-message')).toHaveCount(before);
 const member=await join('observer',7462);await member.locator('#question-view').click();await expect(member.locator('#questions-list')).toContainText('独立问题：解释事务');await expect(member.locator('#ask-form')).toBeHidden();await member.locator('#question-member').selectOption('1001');await expect(member.locator('#questions-list')).toContainText('暂无问答记录');await member.locator('#question-member').selectOption('1002');await expect(member.locator('#questions-list')).toContainText('解释事务');await expect(member.locator('#answers')).toBeHidden();await member.locator('#main-view').click();await expect(member.locator('#questions-panel')).toBeHidden();await expect(member.locator('#answers')).toBeVisible();
});
