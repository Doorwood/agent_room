# agent_room 快速使用

一个 Linux host 创建项目 session；Mac/Linux 用户通过 IP + session_id 申请，host 批准后即可共同提问、安排工作。参与者无需 SSH、系统账号或本机 Codex。

## 一次安装，所有新终端和 session 可用

需要 macOS/Linux、Node.js 20+ 和 npm。推荐直接从官方 npm 包启动安装脚本，无需 sudo 或 GitHub 下载：

```sh
npm exec --yes --registry=https://registry.npmjs.org/ --package=menmu-agent-room@latest -- agent_room-setup
export PATH="$HOME/.local/bin:$PATH"
agent_room dashboard
```

也可以在源码目录或解压后的安装包内执行：

```sh
sh scripts/setup.sh
export PATH="$HOME/.local/bin:$PATH"
```

脚本从公共 npm 仓库安装最新版到 `~/.local/share/agent_room/npm`，并将
`agent_room`、`agent_room-update` 放到 `~/.local/bin`。无需 sudo 或 npm 登录。
它自动配置 Bash/Zsh 启动文件中的 PATH，重复安装不会重复追加配置；新终端直接可用。
其他 shell 请自行将 `~/.local/bin` 加入 PATH。已有全局安装可以保留；请用
`command -v agent_room` 确认使用的是 `~/.local/bin/agent_room`。

每个新 session 直接执行对应的连接命令，无需重新安装：

```sh
agent_room join HOST_IP SESSION_ID --name 你的昵称 --answers
```

检查和自动升级：

```sh
agent_room-update --check   # 仅显示当前版本与 npm 最新版本
agent_room-update           # 检查并升级；已是最新版时跳过安装
```

更新脚本在调用时检查，不创建后台任务或定时任务。网络错误会返回失败；不会主动降级。
更新完成后重启本机客户端并重新打开页面；运行中的 host 需要在合适时间重启才能使用新版。
安装和升级保留已有 session、成员凭证与 Codex 配置，不安装或升级 Codex。
`setup.sh` 是独立脚本，可以单独分发给用户后用 `sh setup.sh` 执行。

## 其他安装方式

有 Node.js 20+ 时可直接安装 npm 包（命令名仍是 `agent_room`）：

```sh
npm install -g menmu-agent-room@latest
```

包内包含 macOS/Linux x64/arm64 二进制，无需 Go 或安装脚本。

解压安装包后执行：

```sh
sh scripts/install.sh
```

默认安装到 `~/.local/bin/agent_room`。安装器按系统选择二进制并验证 SHA-256；只有从不带二进制的源码目录安装时才需要 Go 1.24+。如果命令不在 PATH，按安装器提示执行 `export PATH="$HOME/.local/bin:$PATH"`。

新 Linux host 如缺少兼容 Codex，用：

```sh
sh scripts/install.sh --with-codex
export PATH="$HOME/.local/share/agent_room/codex/node_modules/.bin:$PATH"
~/.local/share/agent_room/codex/node_modules/.bin/codex login
```

这会单独安装已验证版本 `0.153.4`，需要 Node.js/npm。每次 host 启动都从当前 PATH 查找 Codex；找不到就报错，不会自动选择隐藏目录中的版本。已有 Codex 时无需单独安装，先确认同一终端中 `codex --version` 可用且版本兼容。模型和推理档位沿用当前环境 Codex 的配置（包括 CODEX_HOME），agent_room 不写死模型。Mac 客户端只装 agent_room 即可。

## 三步开始

1. host 在 Git 项目目录执行：

   ```sh
   agent_room host .
   ```

2. 把 host 打印的完整 Join 命令发给参与者。参与者在本机执行：

   ```sh
   agent_room join HOST_IP SESSION_ID --name 你的昵称
   ```

3. host 另开终端查看和批准申请：

   ```sh
   agent_room requests
   agent_room approve REQUEST_ID
   ```

参与者自动进入房间，直接输入问题或任务。host 的批准才赋予访问权限，单独知道 session_id 不能读取历史或安排工作。昵称只用于显示，重连身份由本机私有凭证确认。

## 常用命令

| 在哪里 | 命令 | 用途 |
| --- | --- | --- |
| 成员客户端 | 普通文字 | 提问或提交工作，按 FIFO 排队 |
| 成员客户端 | `/status`、`/queue`、`/who` | 查看状态、排队任务、在线成员 |
| 成员客户端 | `/note 文字` | 记录笔记，不触发模型 |
| 成员客户端 | `/steer 文字`、`/cancel` | 引导或取消当前任务 |
| 成员客户端 | `/diff` | 查看项目改动 |
| 成员客户端 | `/quit` | 退出本机客户端 |
| host 终端 | `agent_room requests` | 查看申请和成员记录 |
| host 终端 | `agent_room deny REQUEST_ID` | 拒绝申请 |
| host 终端 | `agent_room revoke REQUEST_ID` | 撤销成员并立即断开其连接 |
| host 终端 | `agent_room session` | 查看当前 session_id 和项目 |

已接受的任务不会因成员断线或撤销自动取消。撤销后的昵称保留用于历史归属，重新申请可使用新昵称。所有批准成员共享同一个 Codex thread、项目目录和队列，工作使用 host 执行用户的权限。

## 重启与多个 session

状态默认按 Git 项目隔离在 `~/.local/share/agent_room/projects` 下。管理命令从同一项目目录执行，或明确传入 `--state`。host 优先复用保存的端口；新会话先尝试 7443，再自动选择空闲端口；显式 `--listen` 不会自动换端口。Ctrl+C 正常停止；在同一项目重新执行 `agent_room host .` 会恢复相同 session 和成员。长期运行可放在 tmux 中。客户端再次执行原来的 join 命令，不必重新审批。

## 可选浏览器窗口

在参与者本机运行 `agent_room join HOST_IP SESSION_ID --name alice --answers`，
或另开终端运行 `agent_room answers HOST_IP SESSION_ID --name alice`。
客户端会打印本机 Browser URL，支持发消息、共享历史、成员筛选和按任务展示进度。
URL 含随机访问标识，请勿公开；保持该客户端运行。纯终端用法不变。

每个 host 进程服务一个项目。另一个项目使用独立状态目录和端口：

```sh
agent_room host /path/to/project --state /absolute/new-state --listen 0.0.0.0:7444
agent_room requests --state /absolute/new-state
agent_room approve REQUEST_ID --state /absolute/new-state
```

如果机器有多个网卡，可用 `--advertise IP:PORT` 指定发给成员的地址。session_id 自带服务器证书指纹，请完整复制。申请 24 小时过期，最多 128 条待批准申请；拒绝多余申请会释放容量。达到历史记录容量后会清理已拒绝和过期申请，保留成员和撤销记录。

## 部署注意

使用你自己的 Linux host、项目路径和 host 输出的 session_id。不要将实际部署地址、会话信息、本机凭证或日志提交到公开仓库。

长期运行时可使用 tmux；请先在普通终端确认 Codex 已登录、网络可用，再使用相同运行环境启动 host。只有可信协作者应被批准加入，所有成员提交的工作都使用 host 执行用户的权限。

## 用本地 Dashboard 管理 room（1.0.4）

```sh
agent_room dashboard
```

浏览器中可查看本机项目、曾使用的成员身份，搜索 room，填写 host/session_id/昵称保存新 room，并直接连接、断开和打开对话。保存身份不等于已获批准；首次连接仍需 host 在终端批准。

默认扫描本机项目状态目录和成员凭证；旧版使用自定义 state 的 host 可通过 `agent_room dashboard --host-state /绝对路径/state` 加入索引。新版本 host 启动会记录其 state 位置。页面中的连接状态只属于当前 Dashboard，不能停止其他终端启动的客户端。

断开不删除凭证、不停止 host、不取消已经提交的任务。关闭网页后连接继续；退出 Dashboard 终端后其连接全部关闭，下次启动仍保留 room，但不自动重连。Dashboard 本身不创建或启动 host，host 继续使用 `agent_room host 项目路径`。

通过 `agent_room --version` 和 `agent_room doctor` 查看本机版本和来源；host 环境检查使用 `agent_room doctor --host`。页面显示运行中客户端版本，磁盘上安装新版本不会改变旧进程；重启本机 dashboard/answers/join 后打开新页面。只改客户端 UI 时无需重启 host。

更新失败保留旧版本；`agent_room-update --rollback` 可以显式切回上一个托管版本。全局 npm 安装仍使用同一 prefix 更新，两种安装可能并存；以 `command -v agent_room` 和 doctor 的输出为准。
