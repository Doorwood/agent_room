# agent_room

A Linux host creates a shared project session. Members on macOS or Linux request access using its IP and session ID, then share a Codex task queue, terminal and browser conversation after host approval.

## Install with npm

Recommended user setup (Node.js 20+, macOS/Linux, no sudo):

```sh
npm exec --yes --registry=https://registry.npmjs.org/ --package=menmu-agent-room@latest -- agent_room-setup
export PATH="$HOME/.local/bin:$PATH"
agent_room dashboard
```

The setup command ships inside the npm package; no GitHub script download is
required. In a source checkout or extracted bundle, use `sh scripts/setup.sh`.
Setup configures Bash/Zsh and preserves existing global installations. Use
`agent_room doctor` to identify the command and version currently in use.

```sh
agent_room --version
agent_room-update --check
agent_room-update
agent_room-update --rollback
```

Updates validate a staged package before switching the managed entry point.
Failed updates keep the old version. Running clients need to be restarted;
updates do not stop hosts or cancel tasks. No background updates are scheduled.

Alternative: global npm installation (use the same npm prefix for updates):

Requires Node.js 20+ and npm:

```sh
npm install -g menmu-agent-room
agent_room help
```

This archive includes macOS/Linux x64/arm64 native binaries. It requires no Go compiler, install scripts, or additional download during installation. The launcher selects the platform and checks its SHA-256 before running. `--ignore-scripts` installations also work.

The npm package name is `menmu-agent-room`; the installed command is `agent_room`. For offline installation, use `npm install -g ./menmu-agent-room-1.0.4.tgz`. To install without administrator access, add `--prefix "$HOME/.local"` and put `$HOME/.local/bin` on PATH.

## Host (Linux)

The host also needs the compatible Codex runtime and a Codex login:

```sh
npm install -g '@openai/codex@0.153.4'
codex --version
codex login
cd /path/to/your/git/project
agent_room host .
```

If the host already has this runtime and login, reuse them. Every host startup
finds Codex on the current PATH and validates compatibility. It does not prefer
private runtime folders or override the model/reasoning effort: those come from
the selected Codex's environment and configuration, including CODEX_HOME.
Missing or unreviewed runtimes fail startup. Members need neither Codex nor a
host OS account. Work runs with the host execution user's authority.

Existing rooms created with the older runtime need a stopped-host backup and
metadata migration; see [runtime upgrade instructions](https://github.com/Doorwood/agent_room/blob/main/docs/runtime-upgrade.md).

## Join and open the browser (member's computer)

Copy the actual host address, including its port, and full session ID:

```sh
agent_room join HOST_IP SESSION_ID --name alice --answers
```

The host approves in another terminal in the same project:

```sh
agent_room requests
agent_room approve REQUEST_ID
```

Already connected in a terminal? Open a separate browser client with:

```sh
agent_room answers HOST_IP SESSION_ID --name alice
```

Startup prints `Browser URL: http://127.0.0.1:<local-port>/<random-access-id>/`. This address is generated on each member's own computer; it cannot be assembled from the host IP and session ID. Keep the client running. Add `--no-open` to print the URL without launching a browser.

The browser supports sending tasks, shared member history, member filters, progress grouped by task, and final answers with collapsible progress. Press Enter to send a message or Alt+Enter to insert a newline. The original terminal remains available.

## Update or uninstall

```sh
npm install -g menmu-agent-room@latest
npm uninstall -g menmu-agent-room
```

Uninstalling the CLI keeps host sessions and client credentials. Native hosts currently running are not replaced until they restart.

## Local room dashboard

Run `agent_room dashboard` once and keep its terminal running. The local page
lists saved member identities and local host projects. Add a host address,
complete session ID and nickname, then connect; first-time members still need
host approval. Open the conversation from the room card. Disconnect only
closes that dashboard's connection, preserving membership and submitted tasks.
Closing the browser tab keeps connections alive; stopping the dashboard process
closes its connections. Rooms remain available next time, without auto-connecting.
Connections opened in other terminals are not controlled by this dashboard.

For a host using a custom state directory from an older version, use
`agent_room dashboard --host-state /absolute/state`. The dashboard does not
start or stop hosts, create host projects, or grant membership approvals.
Use `--no-open` to print the local URL without opening a browser.

The chat page shows its local client version, renders Markdown safely, and
supports code-block copying. Press Enter to send or Alt+Enter for a newline.

## Removing a managed installation

Managed and global npm installations are separate. `npm uninstall -g
menmu-agent-room` removes the global package only. To remove the managed
installation, stop its local clients, remove its recognized `~/.local/bin/agent_room`
and `~/.local/bin/agent_room-update` wrappers, and remove only
`~/.local/share/agent_room/npm`. Keep the surrounding agent_room directory and
user configuration directory to preserve host state and member credentials.
The marked PATH block can remain if you use `~/.local/bin` for other tools.

## 1.0.9 更新

- 聊天页左上角显示当前项目名称，页面顶部与浏览器标签同步展示，方便区分多个项目。
- 项目名称来自 Host 的欢迎信息；Dashboard 打开的聊天页和终端 `answers` 入口均支持，重连时同步更新。
- 优化桌面 Dashboard 的项目卡片、连接状态、加入表单和操作层级，区分打开对话与删除 Room。
- 统一聊天页导航、消息间距、文字对比度、输入框与授权弹窗样式。

本次主要优化桌面使用体验，不改变成员权限、任务执行和个人资源授权流程。未新增数据库迁移。

```sh
npm install -g menmu-agent-room@1.0.9 --registry=https://registry.npmjs.org/
agent_room --version
```

持久化安装用户执行 `agent_room-update`。升级后退出并重启本机 Dashboard / 浏览器客户端，再打开对话；只刷新旧进程提供的网页不会加载新版 UI。Host 1.0.8 已提供项目名称数据，本次 UI 更新不要求中断正在执行任务的 Host。

## 1.0.8 更新

- 聊天页固定导航，滚动聊天时可直接切换成员视图和查看项目任务。
- Host 启动提供同网络只读欢迎页，展示项目信息、安装方法，并通过本机 Dashboard 检测与确认申请连接。
- 新增项目资源授权模式：默认 `host`，Host 可执行 `agent_room resource-mode personal` 切换为各成员本机授权。支持 GitHub、飞书和本机项目文本读取；面板结果默认个人可见，可主动带入主聊天草稿。
- 个人模式下，发送「创建 XX 飞书文档」会使用发送者本机飞书用户账号；缺少授权时向本人询问，凭据留在本机。
- 明确要求提交代码时，向发送者确认 Git 姓名、邮箱，Author 和 Committer 使用其确认署名；不修改 Host Git 配置。按指定整文件快照提交，保留其他文件的暂存内容。仅要求修改代码不自动提交。

Git 署名不代表 GitHub 身份认证；当前只创建本地未签名提交，不执行 hooks、不自动 push。提交前需完成项目检查，并核对同一文件是否含其他成员的修改。个人模式的专用通道校验发送者，但不会在系统层拦截任意 Host Shell/MCP；自然语言操作依赖模型路由指令。

Host、Dashboard 和客户端均需升级并重启；安装更新不会自动中断当前任务。资源面板的隔离总结有独立运行环境要求，详见 [个人资源授权说明](https://github.com/Doorwood/agent_room/blob/main/docs/design/personal-resource-authorization.md)。

```sh
npm install -g menmu-agent-room@1.0.8 --registry=https://registry.npmjs.org/
agent_room --version
```

持久化安装用户执行 `agent_room-update`。欢迎页的本机检测只能确认正在运行的新版 Dashboard，不能扫描已安装软件。

## 1.0.7 更新

- 消息可转为项目任务，保留来源，不重复触发模型；同一工作消息和回答归到同一任务。
- 任务状态与执行同步：待处理、排队、处理中、待验收、异常；协作成员手动标记完成，可重新打开并追加工作。
- 任务详情展示验收标准、执行记录与变更记录；任务和草稿可在刷新、重连后恢复。
- 主聊天和询问者问答显示完整日期与时分秒；参观者与询问者只读查看项目任务。

任务仍共享项目会话、工作目录和队列，查看任务不会切换模型工作。此版本将 Host 数据库迁移到 schema 5；升级前备份状态目录，迁移后旧版 Host 不能直接打开。Host、Dashboard 和客户端均需更新并重启；更新命令不会自动停止运行中的任务。

```sh
npm install -g menmu-agent-room@1.0.7 --registry=https://registry.npmjs.org/
agent_room --version
```

持久化安装用户执行 `agent_room-update`。

## 1.0.6 更新

- 已发送附件支持预览和下载；刷新后恢复图片预览。
- 草稿保存到本机，断开重连、重启客户端后恢复。
- 支持搜索已同步的完整聊天历史；显示任务队列，明确“停止当前任务”不暂停后续任务。
- 新增 visitor（参观者，只读历史）和 asker（询问者，只读问答）权限，由 Host 分配。
- 询问者复用 Host 的 Codex 登录、模型与 session，无需额外 API Key；问答在单独视图展示，协作成员可按成员查看。

```sh
agent_room requests
agent_room approve REQUEST_ID --role visitor
agent_room approve REQUEST_ID --role asker
agent_room role REQUEST_ID --role roommate
```

以上管理命令在 Host 的项目目录执行，可用 `--state DIR` 指定状态目录。

问答会进入共享 Codex 上下文，隐藏页面记录不代表记忆隔离。问答使用只读沙箱，与正式任务串行执行；当前存在 MCP、Apps、Hooks 等无法确认只读的扩展配置时，会拒绝问答，不降级为可写任务。协议与界面已通过模拟器测试，真实 Codex 工具写入阻断尚未实机验收。

更新前备份 Host 状态目录。此版本将数据库迁移到 schema 4，迁移后不能直接用旧版 Host 打开。升级后重启 Host、Dashboard 和客户端才能使用新能力；安装更新不会自动重启运行中的进程。

```sh
npm install -g menmu-agent-room@1.0.6 --registry=https://registry.npmjs.org/
agent_room --version
```

使用持久化安装脚本的用户执行 `agent_room-update` 更新对应安装。

## 1.0.5 更新

- Room 项目名称同步、缓存和搜索；支持删除本机 Room 记录，再次添加复用成员身份。
- 聊天历史向上翻页加载，回复链接在新标签页打开。
- 选择、拖拽文件或粘贴图片上传后发送给模型；单文件最大 20 MiB。附件功能需要 host 和客户端均升级。
- “停止模型”中断当前任务并保持连接；已排队的任务继续执行。

```sh
agent_room-update
agent_room --version
agent_room dashboard
```

更新后重新启动客户端；使用附件功能前也需要重启升级后的 host。
