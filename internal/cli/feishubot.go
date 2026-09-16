package cli

import (
	"agent_romm/internal/daemon"
	"agent_romm/internal/feishubot"
	"agent_romm/internal/network"
	"agent_romm/internal/room"
	"agent_romm/internal/store"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
)

type botBackend struct {
	st     *store.Store
	rid    room.RoomID
	remote *network.Server
	server *daemon.Server
}

func (b botBackend) Authorize(ctx context.Context, uid uint32, work bool) error {
	return b.remote.WithBotMember(ctx, room.UID(uid), work, func(room.Member) error { return nil })
}
func (b botBackend) Status(ctx context.Context) (string, error) {
	s, e := b.server.BotSnapshot(ctx)
	state := string(s.Status)
	if s.Active != nil {
		state = "working"
	}
	return fmt.Sprintf("Room %s\nCodex Agent：%s\n排队工作：%d\n使用 /room 历史 查看消息和已完成回答。", b.rid, state, len(s.Queue)), e
}
func (b botBackend) History(ctx context.Context, q string) (string, error) {
	return b.st.BotHistory(ctx, b.rid, q)
}
func (b botBackend) Work(ctx context.Context, uid uint32, id, text string) (string, error) {
	var accepted room.Acceptance
	err := b.remote.WithBotMember(ctx, room.UID(uid), true, func(m room.Member) error {
		var e error
		accepted, e = b.server.SubmitFromBot(ctx, m, id, text+"\n\n[来源：飞书机器人，发送者已绑定 Room 成员]")
		return e
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("工作已进入项目队列。\n请求：%s\n消息序号：%d\n状态：%s\n这不代表任务已完成；用 /room 历史 查看模型结果。", accepted.ClientMessageID, accepted.Seq, accepted.State), nil
}
func startFeishuBot(ctx context.Context, path, state string, st *store.Store, rid room.RoomID, remote *network.Server, server *daemon.Server, diag io.Writer) (func(), error) {
	c, err := feishubot.Load(path)
	if err != nil {
		return nil, err
	}
	if c.RoomID != string(rid) {
		return nil, fmt.Errorf("机器人配置的 Room ID 与当前项目不一致")
	}
	lark := feishubot.Lark{Config: c}
	if err = lark.Check(ctx); err != nil {
		return nil, err
	}
	b, err := feishubot.New(c, filepath.Join(state, "private", "feishu-bot"), botBackend{st, rid, remote, server}, lark)
	if err != nil {
		return nil, err
	}
	run, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer b.Close()
		err := lark.Consume(run, b.Handle, func(s string) { fmt.Fprintln(diag, "Feishu bot:", s) })
		if err != nil && run.Err() == nil {
			fmt.Fprintln(diag, "Feishu bot stopped:", err)
		}
	}()
	return func() { cancel(); wg.Wait() }, nil
}
