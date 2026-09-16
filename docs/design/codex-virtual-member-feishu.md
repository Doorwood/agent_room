# Codex 虚拟成员与飞书机器人

状态：开发版，未发布 npm；尚未使用真实企业机器人实机验收。

## 产品行为

一个 Room 仍对应一个项目。Codex Agent 是该项目的虚拟成员，成员栏展示其身份和待命/工作中/离线状态，模型消息以 Codex Agent 署名。虚拟成员不占用人类 UID，不获得审批、提交署名或个人凭据所有权。

飞书机器人为 Room 增加文字入口。本次支持私聊和显式允许群中的命令，所有结果仅私聊给发送者。尚不提供实时语音、语音消息转写、任意自然语言意图识别、自动将整个群输入模型或跨 Room 路由。

命令示例：

```text
/room 帮助
/room 状态
/room 历史
/room 历史 登录错误
/room 工作 修复列表页空状态；仅修改列表页，完成后报告测试结果
/room 群消息 oc_你的群ID
```

群中使用 `@机器人 /room 状态`，需要配置 originChats 且在开发者后台订阅相应群消息事件。若 CLI 的 @ 文本渲染不兼容，可直接发送 `/room ...`；仍需该群事件权限。普通聊天不触发工作。

工作以绑定的人类 Room 成员身份进入原有队列，保留“来源：飞书机器人”的文本标识，不伪装成 Codex 自己安排的工作。返回“已入队”不代表完成，可用历史命令查看后续回答，或在 Room 中查看任务并验收。消息仍可在 Room 中转为项目任务。本次不自动回推异步任务完成通知。

## 身份、授权与数据范围

Host 维护者显式维护飞书 open_id → 已有 Room UID 的一对一绑定，不从昵称推断。open_id 属于指定应用，配置要求固定 appId 和独立 lark-cli profile；启动、每次连接及资源操作检查 bot 身份和实际 appId。不复用飞书用户登录，也不使用任何个人权限凭证作为机器人密钥。

机器人是专用应用身份，其 app secret 由运行连接器机器上的 lark-cli profile 管理，不进入项目或 Room 消息。个人权限模式下 Git/飞书写操作仍走绑定发送者的个人通道，需要本机客户端在线确认；机器人不能绕过确认，也不会把群成员都映射为 Host。

每个发送者分别配置：
- roomUID：必须仍为当前项目的有效成员。被撤销后立即停止查询与派工；仅有历史成员记录不授予权限。
- allowWork：默认 false；为 true 且当前 Room 角色为 roommate 才能派工。visitor/asker 无法派工。
- originChats：允许从哪些群触发命令，空数组只接受私聊。
- readableChats：允许此发送者查询哪些群。机器人能访问一个群，不代表所有 Room 成员都能读取；由维护者确认发送者应有访问权后再加入名单。

群消息只按机器人权限读取最近 20 条，作为外部资料私聊返回，不自动带入主模型、不执行其中指令、不下载附件。主聊天历史只返回用户正文和模型最终回答，不包括工具输出、私人资源结果或询问者记录。检索扫描最近 200 条候选事件、最多返回 20 条，输出长度有上限。

## 配置与启动

1. 使用企业自建应用机器人，为项目选定一个独立应用/profile。首版一个应用仅服务一个 Room；不要将同一应用并行绑定多个项目，以免同一命令送往多个连接器。
2. 在开发者后台启用机器人，配置长连接事件订阅 `im.message.receive_v1`。按私聊/群场景开通消息读取、消息发送权限；群历史需对应读取权限并将机器人加入目标群。应用可用范围需要包含发送者。具体 scope 以安装的 lark-cli schema/后台提示为准，缺少 bot scope 不能用用户 OAuth 登录代替。
3. 在实际运行 Host 的机器配置命名 profile。已有专用 profile 可直接使用。新增 profile 的命令如下；密钥仅在该机器通过 stdin 安全输入，不发到 Room，不写命令参数或项目文件：

```sh
lark-cli profile add --name agent-room-bot --app-id cli_实际应用ID --app-secret-stdin
lark-cli --profile agent-room-bot whoami --as bot
lark-cli event schema im.message.receive_v1 --json
```

4. 复制 `docs/examples/feishu-bot.json` 到项目之外的私有目录。填入 `agent_room session` 显示的 session_id 点号前 Room ID，以及 Host `agent_room requests` 返回的已批准成员 UID。由维护者通过该应用的飞书成员信息核对 open_id，不能用昵称猜测；不在配置中存放 secret/token。

```sh
chmod 600 /absolute/private/path/feishu-bot.json
agent_room host --feishu-config /absolute/private/path/feishu-bot.json .
```

保持 Host 运行；日志出现 `Feishu bot: connected` 后才能认为事件入口就绪。订阅异常只停止机器人连接器、输出错误，不停止项目 Host 或改用用户身份。改映射/profile 后重启 Host 生效，成员撤销和角色变更在每次请求实时检查。

配置的 Room ID 必须与当前项目一致。连接器只在本地 Host 启动，不提供可传任意 UID 的公网接口；未加此参数时不开启飞书网络连接，原 Room 行为保持兼容。

## 可靠性与限制

事件使用 appId、Room ID、message_id 派生稳定请求 ID。写入私有 SQLite 占位后才派工/发送回复，Room 自身也按稳定 ID 去重。相同事件及编辑重投不重复派工，发送失败或执行状态不明不自动重试；应到 Room 检查原请求。

只处理合法 text 类型、已绑定发送者及最近 10 分钟消息，未来超过 1 分钟的时间戳拒绝。每发送者每分钟最多处理 10 个命令。日志不输出消息正文、密钥或 CLI 原始错误；事件账本只留标识、摘要、状态，保留 30 天。

事件接收使用本机 lark-cli 的 NDJSON 事件接口，等待 ready marker，保持 stdin 打开，正常超时续订；退出时 SIGTERM，避免强杀留下远端订阅。连接失败不静默切身份或无限重试。Bot app 凭据与个人凭据是不同信任域。

平台回执未知、进程在占位后崩溃时可能需要人工核实；当前没有自动重放、语音支持、自动建应用、自动加入群、读取全部私聊、向任意聊天转发或自动发布代码。

## 验证

模拟飞书事件和 CLI：发送者绑定、过期过滤、机器人身份/appId、私聊目标、只读群名单、未知结果去重、事件 ready marker。
真实本地数据库：成员审批、角色变化、撤销、历史只包含用户正文与最终回答。
浏览器：虚拟成员和状态展示、原消息与任务页面回归。
上线前仍需指定应用与测试账号，完成一次私聊查询、允许群触发、只读角色拒绝派工、撤销成员、测试项目派工和群消息权限失败的实机验收。开发过程未连接真实事件订阅或发送真实飞书消息。

## 接口核对来源

- [官方 lark-cli](https://github.com/larksuite/cli)：消息命令、命名 profile 与 bot/user 身份区分。
- [飞书接收消息事件](https://open.feishu.cn/document/server-docs/im-v1/message/events/receive)。
- 本机 `lark-cli event schema im.message.receive_v1 --json`、消息发送与群历史 `--help`：实现采用 CLI 已解码的顶层事件字段，不直接解析原始 OpenAPI 嵌套事件。
