# README 图片

- `hero.svg`：项目介绍矢量图，手工编写 SVG，可直接维护文字和配色。
- `dashboard.png`、`shared-chat.png`、`create-task.png`、`project-tasks.png`：1.0.7 页面截图，由真实 Dashboard / answerwindow UI 渲染，数据来自 `internal/dashboard/testdata/ui` 的隔离测试程序。
- 所有地址、session ID、成员、项目和消息均为测试数据；没有真实账号授权、生产聊天或真实模型调用。
- 截图不是未来功能效果图。模型结果由测试程序模拟，任务创建、完成、追加执行以及页面状态使用真实项目代码。

复现（项目根目录）：

```bash
npm ci
npx playwright install chromium
go build -ldflags '-X agent_romm/internal/buildinfo.Version=1.0.7' -o dist/dashboard-ui-fixture ./internal/dashboard/testdata/ui
node scripts/capture-readme.cjs
```

Linux 需要安装 Chromium 的系统依赖。脚本启动单独的临时测试进程，生成图片后自动关闭；不会连接或重启现有 Host。
