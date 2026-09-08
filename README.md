# agent_room

Source repository: [Doorwood/agent_room](https://github.com/Doorwood/agent_room).
The CLI and repository are named `agent_room`.

Early-stage software for trusted collaborators. Approved members can request
work with the host execution owner's full authority. See [SECURITY.md](SECURITY.md).

Create one project session on a Linux host. Participants join from macOS or Linux using the host IP and session ID, request admission, then share a persistent Codex conversation and FIFO work queue after host approval. Participants need no SSH credentials, OS accounts, or local Codex installation.

## Install

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

With Node.js 20+ and npm, install the platform-bundled CLI:

```sh
npm install -g menmu-agent-room@latest
```

This includes macOS/Linux x64/arm64 native binaries and verifies their checksums
without install scripts or a Go compiler. See [npm usage](npm/README.md).

Unpack the binary bundle and run:

```sh
sh scripts/install.sh
```

From a source checkout, the same command builds with Go 1.24+. The binary bundle contains macOS/Linux amd64/arm64 executables and checksums; it needs no Go toolchain. Default installation directory: `~/.local/bin`.

On the Linux execution host, optionally install the dedicated compatible Codex runtime (requires Node.js/npm):

```sh
sh scripts/install.sh --with-codex
export PATH="$HOME/.local/share/agent_room/codex/node_modules/.bin:$PATH"
~/.local/share/agent_room/codex/node_modules/.bin/codex login
```

Every host startup resolves `codex` from the current `PATH` and checks its version.
Missing Codex causes an explicit startup error; private runtime directories are
never selected automatically. If Codex is already on PATH, no separate install
is needed. The currently validated CLI version is `0.153.4`; other versions are
rejected until protocol compatibility is reviewed. Model and reasoning effort
are not hard-coded by agent_room: the selected Codex loads its own configuration
and existing login from the current environment (including `CODEX_HOME`).

## Start, join, approve

Host, from a Git project:

```sh
agent_room host .
```

Copy the printed Join command to the participant's machine, choosing a display name:

```sh
agent_room join HOST_IP SESSION_ID --name alice
```

The participant waits for approval. In a second host terminal:

```sh
agent_room requests
agent_room approve REQUEST_ID
```

Approval connects the participant automatically. Enter a question or task as ordinary text. `/status`, `/queue`, `/who`, `/diff`, `/note TEXT`, `/steer TEXT`, `/cancel`, and `/quit` are available. All approved participants can schedule work and control the active turn. Work runs as the execution owner on the host project.

## Manage

```sh
agent_room deny REQUEST_ID
agent_room revoke REQUEST_ID
agent_room session
```

Revocation disconnects that member's current clients and rejects further connections. It does not cancel work already accepted into the shared queue; use `/cancel` for the active turn. A revoked nickname remains reserved for transcript consistency; a fresh application can use a new name.

Ctrl+C stops the host cleanly. Run it in tmux to keep it alive after disconnecting. Start it with the same project and state to reuse the session and membership approvals. Client credentials and replay positions persist locally.

Default state is isolated by canonical Git project under `~/.local/share/agent_room/projects`, outside the project. Run management commands from the same project or pass `--state`. The host reuses its saved port; a new host tries 7443 and then an available port. An explicit `--listen` disables automatic fallback. For an explicit session directory and port:

```sh
agent_room host /path/to/other-project --state /absolute/other-state --listen 0.0.0.0:7444
agent_room requests --state /absolute/other-state
agent_room approve REQUEST_ID --state /absolute/other-state
```

Use `--advertise IP:PORT` if the automatically selected interface is not reachable by participants. The chosen TCP port must be reachable; no SSH forwarding is used. `session_id` includes the host certificate fingerprint, so copy it completely. The ID permits requesting admission, not accessing the room. Connections use TLS 1.3; only credential hashes are stored on the host. Pending requests expire in 24 hours; at most 128 may be pending and 64 connections may be open. The host can deny unwanted requests to release capacity. Members and revocation records are retained, with a 4096-record admission cap and pruning of rejected/expired history at capacity.

The default host flow creates only the current owner's local room record. Approved remote members have application identities, never Unix accounts. A host/session serves one Git root and at most one active Codex turn. Transcript attribution is not tamper-proof against the execution owner.

## Build and test

```sh
go test ./...
go test -race ./...
go vet ./...
sh scripts/package.sh
npm run test:npm
node --test internal/answerwindow/group.test.mjs
npm run pack:npm
npm ci
npx playwright install chromium
npm run test:browser
```

Packaging writes `dist/agent-room-bundle.tar.gz`. No publication occurs automatically. Legacy `agent_romm` SSH commands remain supported; see [legacy core notes](docs/legacy-core-mvp.md). The Go module and source entry point retain their original `agent_romm` spelling.

[中文快速使用文档](docs/QUICKSTART.zh-CN.md)

## Optional local browser client

Run `agent_room join HOST_IP SESSION_ID --name alice --answers`, or
`agent_room answers HOST_IP SESSION_ID --name alice` in another terminal.
The client prints its own local Browser URL with a random access ID; keep it
private and keep that client running. It supports shared history, sending tasks,
member filters, and progress grouped by task. Terminal-only usage is unchanged.

## License and contributions

Released under the [MIT License](LICENSE). See [CONTRIBUTING.md](CONTRIBUTING.md)
for development checks and contribution guidelines. Codex and third-party
dependencies retain their own licenses and terms.

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
