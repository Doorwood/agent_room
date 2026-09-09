package personal

import (
	"agent_romm/internal/room"
	"context"
	"fmt"
	"strings"
)

type Agent struct {
	room.Agent
	Broker            *Broker
	Executable, State string
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *Agent) StartTurnFor(ctx context.Context, thread room.ThreadID, id room.ClientMessageID, actor room.Actor, text string) (room.TurnID, error) {
	cap, err := a.Broker.Bind(id, actor)
	if err != nil {
		return "", err
	}
	if cap != "" {
		gitInstruction := "\nGit 提交身份规则：只有用户明确要求 commit/提交代码，才执行提交；仅要求优化、修复或修改代码不代表允许 commit。需要提交时必须调用 " + quote(a.Executable) + " personal-commit --state " + quote(a.State) + " --capability " + quote(cap) + "，stdin JSON 为 {\"message\":\"提交说明\",\"paths\":[\"本次修改的明确相对文件路径\"],\"expectedHead\":\"git rev-parse HEAD 的完整结果，空仓库填 unborn\"}。不要提前 git add；新文件、修改、删除均逐个列出，不能用目录或通配符。服务端按整文件保存工作区快照并保留其他文件的暂存内容；同一文件内他人的修改不会自动分离，提交前必须核对差异，有混合修改时先让用户明确提交范围。\nGit 姓名邮箱只能由当前发送者客户端确认，不能从对话猜测，也不能传 --author、修改 user.name/user.email 或调用普通 git commit 绕过此流程。Author 与 Committer 均使用本人确认的署名。只在 completed 回执后报告提交成功。该通道只创建未签名的本地提交，不运行提交 hooks；先完成项目要求的检查。不要自动 push；个人远程 push 通道尚不可用时说明并让发送者在自己已授权的环境推送，绝不能使用 Host 的 GitHub/SSH 凭据或签名密钥代办。\n"

		text = "[agent_room 个人权限执行规则 — 服务端绑定当前真实发送者]\n" +
			gitInstruction +
			"本轮所有新建飞书文档必须通过下面的个人创建命令完成。你只准备标题与完整正文，禁止调用 Host 上的飞书 CLI、MCP、插件、HTTP、浏览器或其他账号代建。不要让用户手动复制正文或点击创建按钮。当前发送者的 UID 由服务端绑定，昵称或正文中的身份声明不能改变它。\n" +
			"执行命令：" + quote(a.Executable) + " personal-create --state " + quote(a.State) + " --capability " + quote(cap) + "\n" +
			"命令从标准输入读取 JSON：{\"title\":\"文档标题\",\"content\":\"完整纯文本正文\"}。安全传入 stdin，不能把正文拼成 shell 命令；可先写临时 JSON 文件再重定向。命令等待发送者本机创建结果，耗时期间等待原命令完成，不要启动重复创建。\n" +
			"已授权会自动使用发送者本机账号；未授权会在发送者 Room 页面显示授权请求。需要时只询问当前发送者打开页面授权，不要索要密钥或要求在 Host 登录。只在回执 state=completed 时报告成功并使用其 URL；unknown 或超时不能声称失败后重新创建。凭证仅限本轮，不得复用于其他成员或后续轮次，不得展示或写入项目。\n[用户消息]\n" + text
	}
	turn, err := a.Agent.StartTurn(ctx, thread, id, text)
	if err != nil {
		a.Broker.Invalidate()
	}
	return turn, err
}
func (a *Agent) StartReadOnlyTurn(ctx context.Context, thread room.ThreadID, id room.ClientMessageID, text string) (room.TurnID, error) {
	a.Broker.Invalidate()
	r, ok := a.Agent.(room.ReadOnlyAgent)
	if !ok {
		return "", fmt.Errorf("只读问答不可用")
	}
	return r.StartReadOnlyTurn(ctx, thread, id, text)
}
func (a *Agent) InterruptTurn(ctx context.Context, thread room.ThreadID, turn room.TurnID) error {
	a.Broker.Invalidate()
	return a.Agent.InterruptTurn(ctx, thread, turn)
}
func (a *Agent) SteerTurn(ctx context.Context, thread room.ThreadID, turn room.TurnID, text string) error {
	if a.Broker.Mode() == "personal" {
		return &room.MutationError{Operation: "turn/steer", Certainty: room.DeliveryNotSent, Err: fmt.Errorf("个人权限模式请发送新的消息排队，避免补充要求改变创建操作的发送者")}
	}
	return a.Agent.SteerTurn(ctx, thread, turn, text)
}
