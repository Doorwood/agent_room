// Test-only UI fixture. It does not start Codex or execute submitted instructions.
package main

import (
	"agent_romm/internal/answerwindow"
	"agent_romm/internal/dashboard"
	"agent_romm/internal/network"
	"agent_romm/internal/room"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dir, err := os.MkdirTemp("", "agent-room-ui-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	catalog := dashboard.Catalog{Home: dir, Config: filepath.Join(dir, "config")}
	session := strings.Repeat("a", 32) + "." + strings.Repeat("b", 64)
	if _, err := catalog.Add("127.0.0.1:7443", session, "alice"); err != nil {
		panic(err)
	}
	connector := func(ctx context.Context, r dashboard.Room, update dashboard.Update) error {
		update("pending", "等待 host 审批（测试模拟）", "")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		w, err := answerwindow.Start()
		if err != nil {
			return err
		}
		defer w.Close()
		w.EnableUploads(func(ctx context.Context, name string, size int64, reader io.ReadSeeker) (network.Upload, error) {
			data, err := io.ReadAll(reader)
			if err != nil {
				return network.Upload{}, err
			}
			target := filepath.Join(dir, filepath.Base(name))
			if err = os.WriteFile(target, data, 0600); err != nil {
				return network.Upload{}, err
			}
			return network.Upload{Name: filepath.Base(name), Path: target, Size: size}, nil
		})
		w.Metadata(r.Address, r.Session, r.Name)
		w.Room("ui-room")
		if err := catalog.RememberProject(r.Address, r.Session, "/workspace/demo-project"); err != nil {
			return err
		}
		w.Members([]room.Member{{UID: 1001, Name: r.Name}})
		w.Connection(true)
		seed := func(seq room.Seq, text string) {
			payload, _ := json.Marshal(map[string]string{"body": text, "kind": "prompt", "client_message_id": fmt.Sprint(seq)})
			w.Event(room.DurableEvent{Seq: seq, ActorUID: 1001, Kind: "message/accepted", Payload: payload, CreatedAt: time.Now()})
		}
		seed(1, "一起看看项目的下一步")
		w.Add(room.DurableEvent{Seq: 2, CreatedAt: time.Now()}, "## 已准备好\n\n支持 **协作** 与 [文档](https://example.com/docs)。\n\nhttps://example.com/plain\n\n`https://example.com/inline`\n\n```sh\nagent_room dashboard\n```\n\n| 项目 | 状态 |\n| --- | --- |\n| 页面 | 就绪 |\n\n<script>window.pwned=1</script>\n\n[危险链接](javascript:alert(1))\n\n![不加载图片](https://example.com/tracking.png)")
		submissions := w.EnableChat()
		update("connected", "", w.URL())
		seq := room.Seq(3)
		if r.Name == "history" {
			for ; seq <= 132; seq++ {
				seed(seq, fmt.Sprintf("历史消息 %d", seq))
			}
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case submission := <-submissions:
				if submission.Method == "cancel" {
					w.Event(room.DurableEvent{Seq: seq, Kind: "turn/interrupted", Payload: json.RawMessage(`{"turn_id":"ui-active"}`)})
					seq++
					w.ActiveTurn("")
					submission.Result <- nil
					continue
				}
				seed(seq, submission.Text)
				seq++
				if submission.Text == "测试长任务" {
					body, _ := json.Marshal(map[string]string{"turn_id": "ui-active", "client_message_id": fmt.Sprint(seq - 1)})
					w.Event(room.DurableEvent{Seq: seq, Kind: "turn/running", ActorUID: 1001, Payload: body})
					seq++
					w.ActiveTurn("ui-active")
				}
				submission.Result <- nil
			}
		}
	}
	server, err := dashboard.Start(ctx, catalog, connector)
	if err != nil {
		panic(err)
	}
	defer server.Close()
	fmt.Println(server.URL())
	<-ctx.Done()
}
