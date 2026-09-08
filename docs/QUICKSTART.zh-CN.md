# agent_room 快速使用

一个 Linux host 创建项目 session；Mac/Linux 用户通过 IP + session_id 申请，host 批准后即可共同提问、安排工作。参与者无需 SSH、系统账号或本机 Codex。

## 安装

解压安装包后执行：

```sh
sh scripts/install.sh
```

默认安装到 `~/.local/bin/agent_room`。安装器按系统选择二进制并验证 SHA-256；只有从不带二进制的源码目录安装时才需要 Go 1.24+。如果命令不在 PATH，按安装器提示执行 `export PATH="$HOME/.local/bin:$PATH"`。

新 Linux host 如缺少兼容 Codex，用：

```sh
sh scripts/install.sh --with-codex
~/.local/share/agent_room/codex/node_modules/.bin/codex login
```

这会单独安装固定版本 `0.151.0-alpha.7.2`，需要 Node.js/npm；host 自动找到专用版本，已有登录可直接复用。Mac 客户端只装 agent_room 即可。

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

host 默认监听 7443，状态保存在 `~/.local/share/agent_room/host`。Ctrl+C 正常停止；在同一项目重新执行 `agent_room host .` 会恢复相同 session 和成员。长期运行可放在 tmux 中。客户端再次执行原来的 join 命令，不必重新审批。

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
