# 使用发送者的 GitHub 授权推送

版本：1.0.10。需要升级并重启 Host 和发送者的 Dashboard / 浏览器客户端。

## 用户流程

1. Host 将项目设置为 `agent_room resource-mode personal`。
2. 发送者在自己的电脑安装 Git 2.32+ 和 GitHub CLI，执行 `gh auth login --hostname github.com --web` 完成登录。可通过本机启动环境的 `AGENT_ROOM_GITHUB_CLI` 指定 GitHub CLI 的可执行路径。
3. 在 Room 中明确提出推送要求，例如「把当前提交推送到 Doorwood/agent_room 的 main 分支」。仅修改代码、仅 commit 不代表允许 push；目标不明确时模型需要询问。
4. 模型调用本轮专用 `personal-push`，仅提交 `repository`（owner/repo）、`branch` 和完整 SHA-1 `commit`。Host 校验其为当前项目 HEAD，将这个固定提交及其可达历史生成 Git pack。
5. 请求只发给已认证发送者的一个客户端实例。页面显示仓库、分支、commit、提交统计和传输大小；点击「核对我的 GitHub 账号和权限」读取本机实际 GitHub 用户，校验目标仓库写权限和远端分支现状。
6. 发送者核对账号、目标和远端原提交后，点击「确认使用我的账号推送本次提交」。每次推送分别确认，不复用飞书授权、Git 署名或上一轮推送许可。
7. 客户端下载并校验 pack，在新的临时裸仓库导入对象，使用发送者的本机凭据推送固定 commit。主聊天返回结果和提交链接。资源授权面板可查看最近 10 次本机推送回执。

Git Author / Committer 是提交署名；GitHub 推送账号来自本机登录，两者分别确认。推送不会改写已有提交的作者、签名和哈希。

## 凭据与执行边界

- GitHub CLI 在发送者机器读取凭据。客户端将凭据短暂保存在内存，并以当前账号快照验证 GitHub `/user` 和仓库权限；确认后如果本机登录令牌或账号变化则重新核对。Host 不接收 token、SSH 密钥或本机 GitHub 配置。
- 仅支持 github.com 的仓库与普通分支；目标不接受 URL、任意主机、命令、环境变量、标签、删除或强制覆盖参数。GitHub 分支保护、组织 SSO、工作流文件权限等仍由 GitHub 决定。
- 客户端要求 Git 支持 `GIT_CONFIG_GLOBAL`，实际探测隔离能力；关闭系统及全局 Git 配置、hooks、签名、递归子模块、标签跟随和 HTTP 跳转。不使用用户已有项目目录、不 checkout、不运行项目脚本。
- 固定凭据助手仅为 `https://github.com/owner/repo.git` 的精确目标返回凭据。token 通过短生命周期子进程环境传递，不写命令参数或临时文件，不透传 Git 原始输出。
- 临时裸仓库在正常完成或取消后删除。意外强制终止可能遗留私有临时目录，其中含提交对象、不含写入的凭据。
- 本功能是受控的个人推送通道；主模型使用专用命令仍依赖每轮路由指令，不是对任意 Host Shell/MCP 的系统级降权。

## 并发、重试与范围

确认时记录远端原 HEAD。执行前检查原 HEAD 是目标提交的祖先，随后通过明确的 `--force-with-lease=目标分支:原HEAD` 比较远端值；祖先检查禁止改写历史，lease 拒绝确认后其他人更新的分支。不存在的分支使用空原值，仅能在仍不存在时创建。远端已是目标 commit 时只验证，不再更新。

Host 每轮只接受一个明确推送目标；提交包最多 32 MiB，按 128 KiB 分块经已固定 TLS 证书的成员通道传输，校验总大小与 SHA-256。包括目标 commit 的全部可达历史，不包括未提交文件，也不导出无关分支引用。大仓库、浅克隆或缺失对象可能需要在用户自己的项目环境处理。

发送者与客户端实例绑定不可由昵称或模型改写；其他成员、询问者、参观者、其他客户端、失效凭证和错误分块偏移均拒绝。取消、角色/模式改变、原工作结束或连接检查失败会停止后续步骤并取消进程组。已经送达远端的更新无法撤销。

每次外部写入前，在发送者本机私有 SQLite 中持久化占位。重复请求只返回原回执；超时、断线、不完整响应或远端验证失败标记「待核实」，不自动重新推送。只有执行返回成功且查询远端分支等于指定 commit 才报告完成。请求最长等待 5 分钟，客户端执行上限 4 分钟，单次 Git 命令最多 90 秒。

## 验证与当前限制

- 临时真实 Git 仓库：生成/校验/导入 pack、保留 commit 内容与哈希、新分支、快进及远端竞争时 lease 拒绝。
- 模拟 GitHub 身份与接口：账号变化、权限不足、非快进、取消、确认过期、回执去重、未知结果不重试，回执与序列化审批对象不包含 token。
- 真实本地 TLS 通道：跨用户及只读角色拒绝、分块传输与完整性、结果返回；Broker 测试检查客户端绑定、错误目标回执和旧凭证。
- 浏览器：模拟账号核验返回，检查缺权限提示、完整目标预览、未确认不推送、确认请求绑定账号。
- 未对真实 GitHub 仓库执行推送，也未通过真实模型完成端到端试用。本机 Git 2.20 用于 pack / 临时远端测试，生产个人推送会拒绝该版本。

参考：[Git push](https://git-scm.com/docs/git-push)、[Git 2.32 配置隔离支持](https://raw.githubusercontent.com/git/git/v2.32.0/Documentation/RelNotes/2.32.0.txt)、[GitHub CLI 本机令牌读取](https://cli.github.com/manual/gh_auth_token)、[GitHub 用户身份接口](https://docs.github.com/en/rest/users/users#get-the-authenticated-user)。
