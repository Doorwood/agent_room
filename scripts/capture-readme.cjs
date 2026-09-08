// Capture the real UI using isolated, synthetic fixture data. No Codex is started.
// Build dist/dashboard-ui-fixture before running this script.
const { chromium, expect } = require('@playwright/test');
const { spawn } = require('node:child_process');
const { mkdirSync } = require('node:fs');
const path = require('node:path');

(async () => {
  const fixture = spawn(path.resolve('dist/dashboard-ui-fixture'), [], {stdio: ['ignore', 'pipe', 'pipe']});
  let browser;
  try {
    const url = await new Promise((resolve, reject) => {
      let output = '';
      fixture.stdout.on('data', b => { output += b; if (output.includes('\n')) resolve(output.trim()); });
      fixture.once('error', reject);
      fixture.once('exit', code => reject(new Error('Fixture exited: ' + code)));
    });
    browser = await chromium.launch();
    const context = await browser.newContext({viewport: {width: 1360, height: 1000}, deviceScaleFactor: 1, timezoneId: 'Asia/Shanghai'});
    const errors = [];
    context.on('page', page => page.on('pageerror', e => errors.push(e.message)));
    const page = await context.newPage();
    mkdirSync('docs/images', {recursive: true});
    await page.goto(url);
    await page.locator('#address').fill('127.0.0.1:7470');
    await page.locator('#session').fill('3'.repeat(32) + '.' + '4'.repeat(64));
    await page.locator('#name').fill('tasks');
    await page.locator('#join').click();
    const entry = page.locator('.room').filter({hasText: '127.0.0.1:7470'});
    await expect(entry.locator('.status')).toHaveText('已连接');
    await page.screenshot({path: 'docs/images/dashboard.png', fullPage: true});
    const opened = page.waitForEvent('popup');
    await entry.getByRole('link', {name: '打开对话 ↗'}).click();
    const chat = await opened;
    await expect(chat.locator('.user-message .convert-task')).toBeVisible();
    await expect(chat.locator('.model-message .answer-text')).toContainText('实现完成');
    await chat.screenshot({path: 'docs/images/shared-chat.png', fullPage: true});
    await chat.locator('.user-message .convert-task').click();
    await chat.locator('#task-title').fill('登录体验优化');
    await chat.locator('#task-acceptance').fill('登录成功可进入首页；错误提示清晰；相关检查通过。');
    await chat.locator('#convert-task').screenshot({path: 'docs/images/create-task.png'});
    await chat.locator('#create-task').getByRole('button', {name: '创建任务', exact: true}).click();
    await expect(chat.locator('#task-state')).toHaveText('待验收');
    await chat.locator('#task-text').fill('补充异常登录场景的测试，并说明验证结果。');
    await chat.locator('#task-send').click();
    await expect(chat.locator('#task-runs article')).toHaveCount(2);
    await chat.getByRole('button', {name: '标记任务完成', exact: true}).click();
    await expect(chat.locator('#task-state')).toHaveText('已完成');
    // Capture the full feature panel, not the browser chrome or synthetic session key.
    await chat.locator('#tasks-panel').screenshot({path: 'docs/images/project-tasks.png'});
    if (errors.length) throw new Error(errors.join('\n'));
    console.log('Captured 4 README screenshots from the 1.0.7 UI fixture.');
  } finally {
    if (browser) await browser.close();
    if (fixture.exitCode === null) {
      const exited = new Promise(resolve => fixture.once('exit', resolve));
      fixture.kill('SIGTERM');
      await exited;
    }
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
